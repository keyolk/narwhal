// Package mcp implements a minimal MCP server over stdio so an interactive
// Claude Code session can drive Narwhal directly.
//
// The protocol surface Claude Code needs is small — initialize, tools/list,
// tools/call — so this is hand-rolled JSON-RPC rather than a dependency.
// Keeping it dependency-free also keeps `narwhal` a single static binary,
// which matters because Claude Code launches it as a subprocess.
//
// The server is a thin client of the daemon's /control API. It holds no
// state of its own: Claude Code may restart it at will, and the daemon
// remains the single owner of runs, workers, and radio history.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	protocolVersion = "2025-06-18"
	serverName      = "narwhal"
	serverVersion   = "0.1.0"
)

// Server speaks MCP on stdio and forwards work to the daemon over HTTP.
type Server struct {
	in        io.Reader
	out       io.Writer
	daemonURL func() (string, error)
	client    *http.Client
}

// New returns a server. daemonURL is resolved lazily on each call so the
// daemon can be started after Claude Code has already spawned this process.
func New(in io.Reader, out io.Writer, daemonURL func() (string, error)) *Server {
	return &Server{
		in:        in,
		out:       out,
		daemonURL: daemonURL,
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads newline-delimited JSON-RPC from stdin until EOF.
func (s *Server) Serve() error {
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "parse error: "+err.Error())
			continue
		}
		s.dispatch(req)
	}
	return scanner.Err()
}

func (s *Server) dispatch(req rpcRequest) {
	switch req.Method {
	case "initialize":
		s.writeResult(req.ID, map[string]any{
			"protocolVersion": protocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{
				"name": serverName, "version": serverVersion,
			},
		})
	case "notifications/initialized":
		// Notification: no response.
	case "tools/list":
		s.writeResult(req.ID, map[string]any{"tools": toolDefinitions()})
	case "tools/call":
		s.handleToolCall(req)
	case "ping":
		s.writeResult(req.ID, map[string]any{})
	default:
		// Notifications carry no id and must not be answered.
		if len(req.ID) == 0 {
			return
		}
		s.writeError(req.ID, -32601, "method not found: "+req.Method)
	}
}

func (s *Server) handleToolCall(req rpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "invalid params: "+err.Error())
		return
	}

	text, err := s.callTool(params.Name, params.Arguments)
	if err != nil {
		// Tool errors are reported in-band so the model can react to them
		// rather than the whole request failing.
		s.writeResult(req.ID, map[string]any{
			"content": []map[string]any{{"type": "text", "text": "error: " + err.Error()}},
			"isError": true,
		})
		return
	}
	s.writeResult(req.ID, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
}

func (s *Server) writeResult(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *Server) writeError(id json.RawMessage, code int, msg string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg}})
}

func (s *Server) write(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		return
	}
	fmt.Fprintf(s.out, "%s\n", data)
}
