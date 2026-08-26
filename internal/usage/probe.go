package usage

import (
	"log"

	"github.com/keyolk/narwhal/internal/broker"
)

// TranscriptProbe measures a task by reading its worker's Claude session
// transcript. This is the probe narwhal installs in production; tests
// that care about accounting install a fake instead.
type TranscriptProbe struct{}

// TaskUsage implements broker.UsageProbe.
//
// Returns nil — "not measurable" — rather than a zero tally whenever the
// transcript is missing or unreadable, so an unmeasured task stays
// distinguishable from a free one in the snapshot. The two sessions on
// this machine whose transcripts have been removed are exactly this case,
// and reporting them as costing nothing would quietly shrink the total.
func (TranscriptProbe) TaskUsage(runID, taskID string) *broker.Usage {
	t, err := ForTask(runID, taskID)
	if err != nil {
		// Worth a line: a transcript that exists and will not parse is a
		// gap in the accounting, and the snapshot records it only as an
		// absence.
		log.Printf("[usage] %s/%s: reading transcript: %v", runID, taskID, err)
	}
	if t.Empty() {
		return nil
	}
	return &broker.Usage{
		ServedModel:         t.Model,
		ServedModels:        t.Models,
		InputTokens:         t.InputTokens,
		OutputTokens:        t.OutputTokens,
		CacheCreationTokens: t.CacheCreationTokens,
		CacheReadTokens:     t.CacheReadTokens,
		Turns:               t.Turns,
	}
}

// NewBroker returns a broker that measures every run it creates.
//
// This exists so that "a broker accounts for what it spends" is one
// decision rather than four. broker.New() plus a forgotten
// SetUsageProbe is the shape of defect this codebase has hit before —
// Model was omitted from one of two snapshot builders and 143 tasks
// recorded no tier as a result. A constructor cannot be half-applied.
func NewBroker() *broker.Broker {
	b := broker.New()
	b.SetUsageProbe(TranscriptProbe{})
	return b
}
