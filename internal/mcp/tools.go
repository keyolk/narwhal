// tools.go defines the MCP tool surface and forwards each call to the
// daemon's /control API.
//
// The tool set is deliberately small. Anything a worker can do for itself
// (radio send, drain, task-done) stays in the worker's wrapper scripts;
// these tools are only for what the operator's own session needs: start
// work, see what came back, steer it, stop it.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name": "narwhal_plan",
			"description": "Decompose a request into a task DAG using a planner " +
				"agent, then let the daemon dispatch workers in parallel. The " +
				"planner decides how to split the work; you observe progress via " +
				"narwhal_status and narwhal_drain. Use this instead of " +
				"narwhal_spawn when the request has genuinely independent parts " +
				"worth decomposing — for work you can finish yourself in a few " +
				"steps, do it directly.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cwd": map[string]any{
						"type":        "string",
						"description": "Absolute path the workers run in (usually the repository root).",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "The overall request the workers serve. Recorded on the run.",
					},
					"planner_model": map[string]any{
						"type":        "string",
						"description": "Model for the planner agent (e.g. opus). Omit for ccproxy rotation.",
					},
					"worker_model": map[string]any{
						"type":        "string",
						"description": "Model for investigation workers (e.g. haiku). Omit for ccproxy rotation.",
					},
					"synthesis_model": map[string]any{
						"type":        "string",
						"description": "Model for the synthesis task (e.g. opus). Defaults to worker_model.",
					},
					"plan_timeout_secs": map[string]any{
						"type":        "integer",
						"description": "Max seconds for the planning phase. Default 300.",
					},
				},
				"required": []string{"cwd", "prompt"},
			},
		},
		{
			"name": "narwhal_spawn",
			"description": "Launch one or more headless Claude Code workers on independent " +
				"sub-tasks. Each worker gets its own radio identity and can share findings " +
				"with peers while it works. Returns the run id and per-worker dispatch state. " +
				"Use this when a request has genuinely independent parts worth investigating " +
				"in parallel — not for work you can finish yourself in a few steps.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"cwd": map[string]any{
						"type":        "string",
						"description": "Absolute path the workers run in (usually the repository root).",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "The overall request these workers serve. Recorded on the run.",
					},
					"run_id": map[string]any{
						"type":        "string",
						"description": "Add workers to an existing run. Omit to create a new run.",
					},
					"workers": map[string]any{
						"type":        "array",
						"description": "One entry per worker.",
						"items": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"name": map[string]any{
									"type":        "string",
									"description": "Short identifier, e.g. auth-audit. Becomes the task id.",
								},
								"assignment": map[string]any{
									"type": "string",
									"description": "What this worker should do. Be specific: name files, " +
										"directories, or subsystems. The worker sees only this text.",
								},
								"deps": map[string]any{
									"type":        "array",
									"items":       map[string]any{"type": "string"},
									"description": "Task ids that must complete first. Omit for independent work.",
								},
							},
							"required": []string{"assignment"},
						},
					},
				},
				"required": []string{"cwd", "workers"},
			},
		},
		{
			"name": "narwhal_drain",
			"description": "Fetch radio messages posted since a cursor. Workers broadcast " +
				"findings here as they work, so draining mid-run shows you what they have " +
				"learned before they finish. Pass the returned cursor to the next call to " +
				"avoid re-reading messages.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string"},
					"after": map[string]any{
						"type":        "integer",
						"description": "Cursor from a previous drain. Omit or 0 to read from the start.",
					},
				},
				"required": []string{"run_id"},
			},
		},
		{
			"name": "narwhal_status",
			"description": "Report task states and active workers. With no run_id, summarizes " +
				"every run the daemon is tracking.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string"},
				},
			},
		},
		{
			"name": "narwhal_send",
			"description": "Post a radio message to workers as the operator. Use urgent " +
				"priority when the message invalidates an assumption a worker is currently " +
				"relying on — workers surface those before starting their next step.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id":  map[string]any{"type": "string"},
					"content": map[string]any{"type": "string"},
					"thread_id": map[string]any{
						"type":        "string",
						"description": "planning, worklog, or results. Defaults to worklog.",
					},
					"mentions": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Agent ids to notify. Omit to broadcast to everyone.",
					},
					"priority": map[string]any{
						"type": "string",
						"enum": []string{"fyi", "normal", "urgent"},
					},
				},
				"required": []string{"run_id", "content"},
			},
		},
		{
			"name": "narwhal_cancel",
			"description": "Kill a run's workers. Tasks keep whatever state they reached; " +
				"cancelling records an operator decision, not a failure of the work.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"run_id": map[string]any{"type": "string"},
				},
				"required": []string{"run_id"},
			},
		},
	}
}

func (s *Server) callTool(name string, args json.RawMessage) (string, error) {
	base, err := s.daemonURL()
	if err != nil {
		return "", fmt.Errorf("%w\n\nStart it with: narwhal daemon start", err)
	}

	switch name {
	case "narwhal_plan":
		return s.post(base+"/api/v1/control/plan", args)
	case "narwhal_spawn":
		return s.spawn(base, args)
	case "narwhal_drain":
		return s.post(base+"/api/v1/control/drain", args)
	case "narwhal_status":
		var a struct {
			RunID string `json:"run_id"`
		}
		_ = json.Unmarshal(args, &a)
		url := base + "/api/v1/control/status"
		if a.RunID != "" {
			url += "?run_id=" + a.RunID
		}
		return s.get(url)
	case "narwhal_send":
		return s.post(base+"/api/v1/control/send", args)
	case "narwhal_cancel":
		return s.post(base+"/api/v1/control/cancel", args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// spawn fills in cwd from the process working directory when the caller
// omitted it, so the model does not have to restate the obvious.
func (s *Server) spawn(base string, args json.RawMessage) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal(args, &payload); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if cwd, _ := payload["cwd"].(string); strings.TrimSpace(cwd) == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("cwd is required and could not be inferred: %w", err)
		}
		payload["cwd"] = wd
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return s.post(base+"/api/v1/control/spawn", body)
}

func (s *Server) post(url string, body []byte) (string, error) {
	if len(body) == 0 {
		body = []byte("{}")
	}
	resp, err := s.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("daemon unreachable: %w", err)
	}
	return readResponse(resp)
}

func (s *Server) get(url string) (string, error) {
	resp, err := s.client.Get(url)
	if err != nil {
		return "", fmt.Errorf("daemon unreachable: %w", err)
	}
	return readResponse(resp)
}

func readResponse(resp *http.Response) (string, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("daemon returned %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	// Re-indent so the model reads structured output rather than one long line.
	var pretty bytes.Buffer
	if json.Indent(&pretty, data, "", "  ") == nil {
		return pretty.String(), nil
	}
	return string(data), nil
}
