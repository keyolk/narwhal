// radio_format.go turns protocol messages into something a person reads.
//
// Workers coordinate over the radio with a pipe-delimited wire format —
// FILE_CLAIM|api|internal/api/router.go and friends. That is the right
// shape for a shell script to emit and a parser to consume, and the wrong
// shape to put on screen: the reader has to mentally split on pipes to
// learn that a worker claimed one file.
//
// Rendering them as sentences also makes the coordination visible at a
// glance, which is the point of having the channel on screen at all.
package main

import (
	"fmt"
	"strings"

	"github.com/keyolk/narwhal/internal/broker"
)

// radioSummary renders a message body for the one-line channel view. The
// second return says whether it was a protocol message, so the caller can
// mark it differently from ordinary worker prose.
func radioSummary(content string) (text string, isProtocol bool) {
	if id, name, _, deps, ok := broker.ParseSplitRequest(content); ok {
		label := id
		if name != "" && name != id {
			label = fmt.Sprintf("%s (%s)", id, name)
		}
		if len(deps) > 0 {
			return fmt.Sprintf("new task %s after %s", label, strings.Join(deps, ", ")), true
		}
		return "new task " + label, true
	}

	if action, taskID, deps, ok := broker.ParseDepEdgeRequest(content); ok {
		verb := "now waits on"
		if action == broker.DepRemovePrefix {
			verb = "no longer waits on"
		}
		return fmt.Sprintf("%s %s %s", taskID, verb, strings.Join(deps, ", ")), true
	}

	if action, taskID, paths, ok := broker.ParseFileClaimRequest(content); ok {
		verb := "claims"
		if action == broker.FileReleasePrefix {
			verb = "releases"
		}
		short := make([]string, 0, len(paths))
		for _, p := range paths {
			short = append(short, shortenPath(p))
		}
		return fmt.Sprintf("%s %s %s", taskID, verb, strings.Join(short, ", ")), true
	}

	if taskID, model, reason, ok := broker.ParseModelEscalateRequest(content); ok {
		tier := model
		if tier == "" {
			tier = "a stronger model"
		}
		s := fmt.Sprintf("%s asks for %s", taskID, tier)
		if reason != "" {
			s += ": " + reason
		}
		return s, true
	}

	// The coordinator's completion-gate notice. It is not a parsed request
	// type — it is posted as prose with a marker prefix — but it shows up
	// on every gated run, so it gets the same treatment.
	if rest, ok := strings.CutPrefix(content, "WAITING|"); ok {
		parts := strings.SplitN(rest, "|", 2)
		if len(parts) == 2 {
			return parts[1], true
		}
	}

	return strings.ReplaceAll(content, "\n", " "), false
}
