// Package usage reads a worker's token consumption out of its Claude
// session transcript.
//
// Narwhal's README makes an economic claim — frontier planner, cheap
// workers, frontier synthesis — and nothing measured it. A run recorded
// which tier each task asked for and never what that cost, so "haiku
// workers are an order of magnitude cheaper" was an assumption about the
// price list rather than an observation about this harness. Worse, the
// tier a task requests is not necessarily the model that serves it:
// ccproxy routes on account and quota, and across the 93 tasks on disk
// that have both a requested tier and a transcript, 5 of the 13 that
// named a tier explicitly were served by the other model. Only the
// transcript knows which one actually ran.
//
// The transcript is the source because it is the only artifact that
// carries usage at all. claude-output.txt is the worker's prose answer;
// the outcome file is its conclusion. Neither counts tokens.
package usage

import (
	"bufio"
	"encoding/json"
	"os"
)

// Tally is what one worker's session consumed.
//
// The four token classes are kept apart rather than summed because they
// are not interchangeable: a cache read is an order of magnitude cheaper
// than the same tokens fresh, and cache_read dominates every narwhal
// worker (1.14B read against 110M input across the corpus). Collapsing
// them into one number would make every run look enormous and would hide
// the thing worth watching, which is output.
type Tally struct {
	// Model is what actually served the session — the transcript's own
	// record, not the tier the task asked for. Empty when the session
	// produced no assistant turn.
	Model string `json:"model,omitempty"`
	// Models lists every model that served this session when more than
	// one did, most-used first. A session normally has one; a fallback
	// mid-session produces two, and that is worth not averaging away.
	Models []string `json:"models,omitempty"`

	InputTokens         int64 `json:"input_tokens,omitempty"`
	OutputTokens        int64 `json:"output_tokens,omitempty"`
	CacheCreationTokens int64 `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     int64 `json:"cache_read_tokens,omitempty"`

	// Turns is the number of distinct assistant API responses, which is
	// the unit a turn budget would be denominated in. It is not the
	// number of lines carrying usage: one response is written to the
	// transcript once per content block, so a response with thinking,
	// text and a tool call appears three times with the usage repeated
	// verbatim. Counting lines overstates by ~2x — measured over the
	// corpus, 15,465 usage lines are 7,543 responses.
	Turns int `json:"turns,omitempty"`
}

// Empty reports whether nothing was counted, which is how a caller tells
// "this worker used no tokens" from "there was no transcript to read".
func (t Tally) Empty() bool { return t.Turns == 0 }

// Add folds another tally in. Used to roll per-task tallies up to a run.
//
// The model fields do not merge: a run has many models by construction,
// and picking one to represent it would be a lie. Run-level model
// attribution is ByModel's job.
func (t *Tally) Add(o Tally) {
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.CacheCreationTokens += o.CacheCreationTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.Turns += o.Turns
}

// transcriptLine is the subset of a transcript record this package reads.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int64 `json:"input_tokens"`
			OutputTokens             int64 `json:"output_tokens"`
			CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// syntheticModel marks a transcript record Claude Code wrote itself
// rather than one an API call produced — an interrupted turn, say. They
// carry a zeroed usage block, so counting them would inflate the turn
// count with turns that never reached a model.
const syntheticModel = "<synthetic>"

// ReadTranscript tallies one session transcript.
//
// Returns a zero Tally and no error when the file does not exist: a task
// that was never dispatched has no transcript, and that is an ordinary
// state rather than a failure. Malformed lines are skipped rather than
// failing the read — the file is appended to while a worker runs, so the
// last line can be a partial write, and refusing to report 200 good turns
// because the 201st is half-flushed serves nobody.
func ReadTranscript(path string) (Tally, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Tally{}, nil
		}
		return Tally{}, err
	}
	defer f.Close()

	var t Tally
	// seen dedups by message.id — see Tally.Turns.
	seen := make(map[string]bool)
	perModel := make(map[string]int)

	sc := bufio.NewScanner(f)
	// Transcript lines carry whole tool results and can be far longer
	// than bufio's 64KB default, which would silently truncate the scan
	// at the first big one. The largest transcript on disk is 8MB, so a
	// line cap of 16MB cannot be reached by a single record.
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			continue
		}
		if line.Type != "assistant" || line.Message.Usage == nil {
			continue
		}
		if line.Message.Model == syntheticModel {
			continue
		}
		id := line.Message.ID
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true

		u := line.Message.Usage
		t.InputTokens += u.InputTokens
		t.OutputTokens += u.OutputTokens
		t.CacheCreationTokens += u.CacheCreationInputTokens
		t.CacheReadTokens += u.CacheReadInputTokens
		t.Turns++
		if m := line.Message.Model; m != "" {
			perModel[m]++
		}
	}
	if err := sc.Err(); err != nil {
		// A partial read is still worth reporting: the caller gets the
		// turns that parsed plus the reason the rest did not.
		return t, err
	}
	t.Model, t.Models = rankModels(perModel)
	return t, nil
}
