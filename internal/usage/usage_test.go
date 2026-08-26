package usage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTranscript builds a transcript file from raw JSONL lines.
func writeTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// assistantLine renders one transcript record the way Claude Code writes
// it: the usage block is repeated verbatim for each content block of the
// same response.
func assistantLine(msgID, model string, in, out, cc, cr int64) string {
	return `{"type":"assistant","message":{"id":"` + msgID + `","model":"` + model +
		`","usage":{"input_tokens":` + itoa(in) +
		`,"output_tokens":` + itoa(out) +
		`,"cache_creation_input_tokens":` + itoa(cc) +
		`,"cache_read_input_tokens":` + itoa(cr) + `}}}`
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// One API response is written to the transcript once per content block —
// thinking, text, tool_use — with the usage repeated. Counting lines
// instead of responses overstates by about 2x across the real corpus.
func TestReadTranscriptCountsOneResponseOnceAcrossItsContentBlocks(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-opus-5", 10, 359, 30480, 18898),
		assistantLine("msg_1", "claude-opus-5", 10, 359, 30480, 18898),
		assistantLine("msg_1", "claude-opus-5", 10, 359, 30480, 18898),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 1 {
		t.Errorf("Turns = %d, want 1 (three content blocks of one response)", got.Turns)
	}
	if got.OutputTokens != 359 {
		t.Errorf("OutputTokens = %d, want 359 (counted once, not tripled)", got.OutputTokens)
	}
	if got.InputTokens != 10 || got.CacheCreationTokens != 30480 || got.CacheReadTokens != 18898 {
		t.Errorf("token classes = in %d cc %d cr %d, want 10/30480/18898",
			got.InputTokens, got.CacheCreationTokens, got.CacheReadTokens)
	}
}

func TestReadTranscriptSumsDistinctResponses(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-opus-5", 10, 100, 5, 7),
		assistantLine("msg_2", "claude-opus-5", 20, 200, 6, 8),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 2 || got.OutputTokens != 300 || got.InputTokens != 30 {
		t.Errorf("got turns=%d out=%d in=%d, want 2/300/30",
			got.Turns, got.OutputTokens, got.InputTokens)
	}
}

// The transcript, not the requested tier, is what says which model ran.
func TestReadTranscriptReportsTheModelThatServed(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-haiku-4-5-20251001", 5, 131, 321, 55284),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q, want the haiku id from the transcript", got.Model)
	}
	if got.Models != nil {
		t.Errorf("Models = %v, want nil when a single model served", got.Models)
	}
}

// A session served by two models must not average them away: the second
// model is the interesting fact, not noise to round off.
func TestReadTranscriptKeepsEveryModelThatServed(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-opus-5", 1, 1, 0, 0),
		assistantLine("msg_2", "claude-haiku-4-5-20251001", 1, 1, 0, 0),
		assistantLine("msg_3", "claude-haiku-4-5-20251001", 1, 1, 0, 0),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Model != "claude-haiku-4-5-20251001" {
		t.Errorf("Model = %q, want the most-used model", got.Model)
	}
	want := []string{"claude-haiku-4-5-20251001", "claude-opus-5"}
	if len(got.Models) != 2 || got.Models[0] != want[0] || got.Models[1] != want[1] {
		t.Errorf("Models = %v, want %v (most-used first)", got.Models, want)
	}
}

// Records Claude Code synthesizes carry a zeroed usage block. Counting
// them would report turns that never reached a model.
func TestReadTranscriptSkipsSyntheticRecords(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-opus-5", 10, 100, 0, 0),
		assistantLine("msg_2", "<synthetic>", 0, 0, 0, 0),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 1 {
		t.Errorf("Turns = %d, want 1 (the synthetic record is not a turn)", got.Turns)
	}
}

func TestReadTranscriptIgnoresNonAssistantRecords(t *testing.T) {
	path := writeTranscript(t,
		`{"type":"user","message":{"content":"go"}}`,
		`{"type":"queue-operation","operation":"x"}`,
		assistantLine("msg_1", "claude-opus-5", 10, 100, 0, 0),
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 1 || got.OutputTokens != 100 {
		t.Errorf("got turns=%d out=%d, want 1/100", got.Turns, got.OutputTokens)
	}
}

// A worker appends to its transcript while it runs, so the last line can
// be a partial write. Refusing the whole read would lose every good turn
// before it.
func TestReadTranscriptSurvivesATruncatedTrailingLine(t *testing.T) {
	path := writeTranscript(t,
		assistantLine("msg_1", "claude-opus-5", 10, 100, 0, 0),
		`{"type":"assistant","message":{"id":"msg_2","mod`,
	)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 1 || got.OutputTokens != 100 {
		t.Errorf("got turns=%d out=%d, want the complete record still counted", got.Turns, got.OutputTokens)
	}
}

// A task that was never dispatched has no transcript. That is an ordinary
// state, not a failure to report.
func TestReadTranscriptOfAMissingFileIsEmptyNotAnError(t *testing.T) {
	got, err := ReadTranscript(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("ReadTranscript of a missing file: %v", err)
	}
	if !got.Empty() {
		t.Errorf("got %+v, want an empty tally", got)
	}
}

// Transcript lines carry whole tool results and routinely exceed bufio's
// 64KB default. A line over that cap used to end the scan silently.
func TestReadTranscriptReadsLinesLargerThanTheScannerDefault(t *testing.T) {
	huge := assistantLine("msg_1", "claude-opus-5", 10, 100, 0, 0)
	// Pad a record past 64KB with a field the decoder ignores.
	pad := `{"type":"assistant","pad":"` + strings.Repeat("x", 200_000) +
		`","message":{"id":"msg_pad","model":"claude-opus-5","usage":{"input_tokens":1,"output_tokens":2,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`
	path := writeTranscript(t, pad, huge)
	got, err := ReadTranscript(path)
	if err != nil {
		t.Fatalf("ReadTranscript: %v", err)
	}
	if got.Turns != 2 {
		t.Errorf("Turns = %d, want 2 — the oversized line must not end the scan", got.Turns)
	}
}

func TestAddRollsTalliesUp(t *testing.T) {
	a := Tally{InputTokens: 1, OutputTokens: 2, CacheCreationTokens: 3, CacheReadTokens: 4, Turns: 1}
	a.Add(Tally{InputTokens: 10, OutputTokens: 20, CacheCreationTokens: 30, CacheReadTokens: 40, Turns: 2})
	if a.InputTokens != 11 || a.OutputTokens != 22 || a.CacheCreationTokens != 33 ||
		a.CacheReadTokens != 44 || a.Turns != 3 {
		t.Errorf("got %+v, want each class summed", a)
	}
}

func TestTierMapsDatedModelIDs(t *testing.T) {
	cases := map[string]string{
		"claude-opus-5":             "opus",
		"claude-haiku-4-5-20251001": "haiku",
		"claude-sonnet-5":           "sonnet",
		"":                          "",
		"some-other-provider/model": "",
	}
	for model, want := range cases {
		if got := Tier(model); got != want {
			t.Errorf("Tier(%q) = %q, want %q", model, got, want)
		}
	}
}
