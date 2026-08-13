package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
	"github.com/keyolk/narwhal/internal/server"
)

// experimentCmd validates the central Narwhal hypothesis: that a directly
// executed `ccproxy claude --print` worker receives a background Bash task
// completion notification when a peer posts a radio message, and can act on
// it while continuing foreground work.
//
// Our earlier Workflow-subagent experiments showed notifications are NOT
// delivered there. This command runs the same scenario in a directly
// executed process to see whether the mechanism works.
//
// Scenario:
//   - receiver: starts `scripts/watch` as a BACKGROUND Bash task, then runs
//     a foreground compute loop, then reports whether it received a
//     completion notification and what the message said.
//   - sender: waits for the receiver's ready marker, then posts an URGENT
//     radio message mentioning the receiver.
//
// Success criteria (all must hold):
//  1. receiver started the watcher as a background task
//  2. the watcher resolved with the sender's message
//  3. the receiver acted on the message (wrote the ack artifact)
func experimentCmd(args []string) {
	fs := flag.NewFlagSet("experiment", flag.ExitOnError)
	cwd := fs.String("cwd", "", "working directory for workers")
	timeout := fs.Duration("timeout", 10*time.Minute, "experiment timeout")
	fs.Parse(args)

	if *cwd == "" {
		*cwd, _ = os.Getwd()
	}

	runID := fmt.Sprintf("exp-%d", time.Now().UnixNano()/1e6)

	b := broker.New()
	reg := broker.NewAgentRegistry()
	run := b.CreateRun(runID, "passive awareness experiment", *cwd, "main")
	run.CreateThread("worklog", "worklog", []string{"receiver", "sender"})

	srv := server.New(b, reg)
	addr, err := srv.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: start broker: %v\n", err)
		os.Exit(1)
	}
	defer srv.Shutdown()

	fmt.Fprintf(os.Stderr, "[narwhal] experiment %s\n", runID)
	fmt.Fprintf(os.Stderr, "[narwhal] broker: %s\n", addr)

	l := launcher.New(addr, runID, *cwd)
	expDir := filepath.Join(l.SessionDir(), "experiment")
	if err := os.MkdirAll(expDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "error: create experiment dir: %v\n", err)
		os.Exit(1)
	}
	ackPath := filepath.Join(expDir, "receiver-ack.json")
	readyPath := filepath.Join(expDir, "receiver-ready")

	receiverAgent := reg.Register("receiver", runID, false)
	senderAgent := reg.Register("sender", runID, false)

	run.AddTask("task-receiver", "receiver", "receive a peer message via background watcher", nil)
	run.AddTask("task-sender", "sender", "send a peer message", nil)

	receiverAssignment := fmt.Sprintf(`RUNTIME EXPERIMENT — follow these steps exactly using real tool calls. Do not simulate.

Step 1. Start the radio watcher as a BACKGROUND Bash task (run_in_background=true):
  bash %s/scripts/watch 0 90000

Step 2. Immediately after the background call returns, create the ready marker with a foreground Bash command:
  touch %s

Step 3. Run this foreground compute command (it takes about 8 seconds):
  python3 -c 'import time; t=time.time(); n=0
while time.time()-t < 8: n+=1
print("foreground-done", n)'

Step 4. Report whether the harness delivered a background task completion notification to you while or after step 3, WITHOUT you polling any file. If you received the watcher output, quote it exactly.

Step 5. If and only if the watcher output contains "ACTION:WRITE_ACK", write the acknowledgement with this exact foreground command:
  python3 -c 'import json; json.dump({"event":"ack","reason":"ACTION:WRITE_ACK"}, open("%s","w"))'

Step 6. Print a final summary with these exact labels, one per line:
  BACKGROUND_NOTIFICATION: yes|no
  WATCHER_OUTPUT: <exact output or none>
  ACK_WRITTEN: yes|no

Do not read the ack file. Do not call the drain script. The point is to test whether the background watcher notification reaches you automatically.`,
		filepath.Join(l.SessionDir(), "agents", "receiver"),
		readyPath,
		ackPath)

	senderAssignment := fmt.Sprintf(`RUNTIME EXPERIMENT — follow these steps exactly using real tool calls. Do not simulate.

Step 1. Wait for the receiver ready marker with this foreground Bash command:
  python3 -c 'import time,os
p="%s"
d=time.time()+120
while time.time()<d and not os.path.exists(p): time.sleep(0.2)
print("ready" if os.path.exists(p) else "timeout")'

Step 2. Wait 3 more seconds so the receiver is inside its foreground work:
  sleep 3

Step 3. Send the radio message with this exact foreground Bash command:
  bash %s/scripts/send worklog "peer finding ACTION:WRITE_ACK" receiver urgent

Step 4. Print the exact output of step 3.`,
		readyPath,
		filepath.Join(l.SessionDir(), "agents", "sender"))

	receiverCfg := launcher.WorkerConfig{
		AgentID:    "receiver",
		TaskID:     "task-receiver",
		Assignment: receiverAssignment,
	}
	senderCfg := launcher.WorkerConfig{
		AgentID:    "sender",
		TaskID:     "task-sender",
		Assignment: senderAssignment,
	}

	receiverDir, err := l.SetupAgent(receiverAgent, receiverCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: setup receiver: %v\n", err)
		os.Exit(1)
	}
	senderDir, err := l.SetupAgent(senderAgent, senderCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: setup sender: %v\n", err)
		os.Exit(1)
	}

	if err := l.Launch(receiverDir, receiverCfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: launch receiver: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[narwhal] launched receiver\n")

	if err := l.Launch(senderDir, senderCfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: launch sender: %v\n", err)
		os.Exit(1)
	}
	fmt.Fprintf(os.Stderr, "[narwhal] launched sender\n")

	if err := l.Wait(*timeout); err != nil {
		fmt.Fprintf(os.Stderr, "[narwhal] %v\n", err)
	}

	// Report durable evidence alongside the run snapshot.
	result := map[string]any{
		"run_id":       runID,
		"session_dir":  l.SessionDir(),
		"ack_exists":   fileExists(ackPath),
		"ready_exists": fileExists(readyPath),
		"snapshot":     run.Snapshot(),
		"receiver_log": filepath.Join(l.SessionDir(), "agents", "receiver", "claude-output.txt"),
		"sender_log":   filepath.Join(l.SessionDir(), "agents", "sender", "claude-output.txt"),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(result)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
