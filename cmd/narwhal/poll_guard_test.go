package main

import (
	"errors"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// twoRunModel watches r1 with two runs listed, ready for selectRun(1).
func twoRunModel() tuiModel {
	runs := []store.LiveRun{
		{RunID: "r1", BrokerURL: "http://x"},
		{RunID: "r2", BrokerURL: "http://x"},
	}
	m := newTUIModel(runs, 0, time.Second, false)
	for i, r := range m.runs {
		if r.RunID == "r1" {
			m.runCur = i
		}
	}
	m.live = m.runs[m.runCur]
	return m
}

func runIndex(m tuiModel, id string) int {
	for i, r := range m.runs {
		if r.RunID == id {
			return i
		}
	}
	return -1
}

func channel(runID string, seqs ...int64) broker.Snapshot {
	s := broker.Snapshot{RunID: runID, State: broker.RunActive,
		Tasks: []broker.TaskSnapshot{{ID: "t-" + runID, State: broker.TaskReady}}}
	for _, q := range seqs {
		s.Messages = append(s.Messages, &broker.Message{Seq: q, Sender: "worker-t-" + runID, Content: "m"})
	}
	return s
}

func TestAPollForThePreviousRunIsDroppedAfterSwitching(t *testing.T) {
	m := twoRunModel()
	_ = m.selectRun(runIndex(m, "r2"))
	if m.live.RunID != "r2" {
		t.Fatalf("watching %q after selectRun", m.live.RunID)
	}

	// r1's poll was still in flight when the switch happened.
	out, _ := m.Update(snapshotMsg{snap: channel("r1", 1, 2, 90), runID: "r1"})
	m = out.(tuiModel)

	if m.snap.RunID != "" || len(m.snap.Tasks) != 0 {
		t.Fatalf("r1's snapshot was drawn on r2: %+v", m.snap)
	}
	// The first-snapshot bookkeeping must still be waiting for r2.
	if m.seenChannel || m.lastSeq != 0 {
		t.Fatalf("r1's channel was absorbed as r2's first snapshot: seen=%v lastSeq=%d", m.seenChannel, m.lastSeq)
	}

	// r2's own first snapshot is absorbed without replaying history.
	out, _ = m.Update(snapshotMsg{snap: channel("r2", 1, 2, 3), runID: "r2"})
	m = out.(tuiModel)
	if m.snap.RunID != "r2" || m.lastSeq != 3 || len(m.pulses) != 0 {
		t.Fatalf("r2's first snapshot: run=%q lastSeq=%d pulses=%d", m.snap.RunID, m.lastSeq, len(m.pulses))
	}
}

func TestAnErrorForThePreviousRunIsDroppedAfterSwitching(t *testing.T) {
	m := twoRunModel()
	_ = m.selectRun(runIndex(m, "r2"))
	out, _ := m.Update(snapshotMsg{runID: "r1", err: errBrokerGone})
	m = out.(tuiModel)
	if m.err != nil {
		t.Fatalf("r1's broker error was reported against r2: %v", m.err)
	}
}

var errBrokerGone = errors.New("broker gone")
