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
	stale := m.pollSeq
	_ = m.selectRun(runIndex(m, "r2"))
	if m.live.RunID != "r2" {
		t.Fatalf("watching %q after selectRun", m.live.RunID)
	}

	// r1's poll was still in flight when the switch happened.
	out, _ := m.Update(snapshotMsg{snap: channel("r1", 1, 2, 90), runID: "r1", seq: stale})
	m = out.(tuiModel)

	if m.snap.RunID != "" || len(m.snap.Tasks) != 0 {
		t.Fatalf("r1's snapshot was drawn on r2: %+v", m.snap)
	}
	// The first-snapshot bookkeeping must still be waiting for r2.
	if m.seenChannel || m.lastSeq != 0 {
		t.Fatalf("r1's channel was absorbed as r2's first snapshot: seen=%v lastSeq=%d", m.seenChannel, m.lastSeq)
	}

	// r2's own first snapshot is absorbed without replaying history.
	out, _ = m.Update(snapshotMsg{snap: channel("r2", 1, 2, 3), runID: "r2", seq: m.pollSeq})
	m = out.(tuiModel)
	if m.snap.RunID != "r2" || m.lastSeq != 3 || len(m.pulses) != 0 {
		t.Fatalf("r2's first snapshot: run=%q lastSeq=%d pulses=%d", m.snap.RunID, m.lastSeq, len(m.pulses))
	}
}

func TestAnErrorForThePreviousRunIsDroppedAfterSwitching(t *testing.T) {
	m := twoRunModel()
	_ = m.selectRun(runIndex(m, "r2"))
	out, _ := m.Update(snapshotMsg{runID: "r1", seq: 1, err: errBrokerGone})
	m = out.(tuiModel)
	if m.err != nil {
		t.Fatalf("r1's broker error was reported against r2: %v", m.err)
	}
}

var errBrokerGone = errors.New("broker gone")

func TestSwitchingBackDropsTheEarlierVisitsPoll(t *testing.T) {
	// r1 → r2 → r1: a poll from the first visit to r1 names the right run
	// but predates the switch, and must not become the first snapshot.
	m := twoRunModel()
	early := m.pollSeq
	_ = m.selectRun(runIndex(m, "r2"))
	_ = m.selectRun(runIndex(m, "r1"))
	out, _ := m.Update(snapshotMsg{snap: channel("r1", 1, 2), runID: "r1", seq: early})
	m = out.(tuiModel)
	if m.seenChannel || len(m.snap.Tasks) != 0 {
		t.Fatalf("the earlier visit's poll was applied: seen=%v tasks=%d", m.seenChannel, len(m.snap.Tasks))
	}
}

func TestTickDoesNotStackPolls(t *testing.T) {
	m := testModel(1, 0)
	m.polling = true
	before := m.pollSeq
	out, _ := m.Update(tickMsg(time.Now()))
	m = out.(tuiModel)
	if m.pollSeq != before {
		t.Fatalf("tick issued poll %d while %d was in flight", m.pollSeq, before)
	}

	// The answer clears the flag and the next tick polls again.
	out, _ = m.Update(snapshotMsg{snap: m.snap, runID: m.live.RunID, seq: before})
	m = out.(tuiModel)
	if m.polling {
		t.Fatal("the newest poll answered but polling is still set")
	}
	out, _ = m.Update(tickMsg(time.Now()))
	m = out.(tuiModel)
	if m.pollSeq != before+1 || !m.polling {
		t.Fatalf("tick after the answer: pollSeq=%d polling=%v", m.pollSeq, m.polling)
	}
}

func TestAnOlderPollAnsweringLastDoesNotRollBack(t *testing.T) {
	// A steer's explicit poll overlaps the tick's: the tick's (older)
	// answer arrives after the steer's (newer) one.
	m := testModel(1, 3)
	_ = m.poll()
	olderSeq := m.pollSeq
	_ = m.poll()
	newerSeq := m.pollSeq

	newer := m.snap
	newer.Messages = append(append([]*broker.Message{}, newer.Messages...),
		&broker.Message{Seq: 4, Sender: "w", Content: "newest"})
	out, _ := m.Update(snapshotMsg{snap: newer, runID: m.live.RunID, seq: newerSeq})
	m = out.(tuiModel)
	out, _ = m.Update(snapshotMsg{snap: channel("r1", 1, 2, 3), runID: m.live.RunID, seq: olderSeq})
	m = out.(tuiModel)

	if len(m.snap.Messages) != 4 || m.radioCur != 3 {
		t.Fatalf("an older poll rolled the view back: %d messages, radioCur=%d", len(m.snap.Messages), m.radioCur)
	}
	if m.polling {
		t.Fatal("polling still set after the newest poll answered")
	}
}
