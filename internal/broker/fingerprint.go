// fingerprint.go identifies a run by what was asked, not when.
//
// A run id is a timestamp, so the system had no way to notice it was being
// asked to do something it was already doing. That is not hypothetical:
// a daemon restart orphaned a run and the same request was submitted again
// four minutes later, and the picker showed two rows that looked identical
// because they were.
//
// This is deliberately advisory. Re-running a request is usually
// legitimate — the code changed, the last attempt failed, or you want to
// see it again — so a fingerprint that *blocked* a duplicate would break
// ordinary use to prevent an occasional mistake. What the caller needs is
// to be told, and then to decide.
//
// It also only catches the duplicates a machine made. Measured against the
// pair that motivated it — the same EKS request resubmitted four minutes
// after a restart orphaned the first — the hashes did not match, because
// the person retyped the prompt and one comma moved:
//
//	...GA) 위에서, Sendbird mesg 클러스터의...
//	...GA) 위에서 Sendbird mesg 클러스터의...
//
// People do not repeat themselves character for character. Normalising
// harder is not the fix: strip enough punctuation to match those two and
// unrelated requests start colliding, which is the worse failure — a
// confident warning pointing at the wrong run. So an exact hash catches
// programmatic resubmission (a retry loop, a script, an MCP client asked
// twice) and honestly misses a human retyping. The defence against the
// human case is elsewhere: the picker now shows an orphaned run as
// recoverable rather than as history.
package broker

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
)

// Fingerprint is a stable hash of the request a run was created from.
//
// Two runs share a fingerprint when they were asked to do the same thing
// in the same place. It does not cover the model tiers: asking the same
// question on a stronger model is a new question in every sense that
// matters to the person asking.
func Fingerprint(cwd, prompt string) string {
	h := sha256.Sum256([]byte(normalizeCWD(cwd) + "\x00" + NormalizePrompt(prompt)))
	return hex.EncodeToString(h[:8])
}

// normalizeCWD resolves a path so /tmp/x and /private/tmp/x — the same
// directory on macOS — do not read as two different requests.
func normalizeCWD(cwd string) string {
	if abs, err := filepath.Abs(cwd); err == nil {
		cwd = abs
	}
	return strings.TrimSuffix(cwd, string(filepath.Separator))
}

// NormalizePrompt strips the parts of a prompt that vary between two
// submissions of the same request.
//
// Prompts are pasted, not typed. A benchmark prompt opens with a hundred
// characters of <uploaded_files> and an absolute scratch path that is
// different every time, so hashing the raw text would give the same
// request a different fingerprint on every submission — which is the one
// way this feature can fail silently.
func NormalizePrompt(prompt string) string {
	p := strings.Join(strings.Fields(prompt), " ")

	// Tool-injected preambles, in the order they nest.
	if i := strings.Index(p, "</uploaded_files>"); i >= 0 {
		p = strings.TrimSpace(p[i+len("</uploaded_files>"):])
	}
	for _, lead := range []string{
		"I've uploaded a code repository in the directory",
		"I have uploaded a code repository in the directory",
	} {
		if strings.HasPrefix(p, lead) {
			rest := strings.TrimSpace(strings.TrimPrefix(p, lead))
			if j := strings.IndexByte(rest, ' '); j > 0 {
				rest = strings.TrimSpace(rest[j:])
			}
			p = rest
		}
	}
	return strings.ToLower(p)
}

// DuplicateOf reports an unfinished run asking the same thing as the given
// request, or "" when there is none.
//
// Only unfinished runs count. Re-running something that already finished
// is the normal way to work — the code changed, or you want to see it
// again — and warning about it would train the reader to ignore the
// warning. What is worth saying is that the same request is *in flight*,
// or was left in flight by a restart and can be recovered instead of
// redone.
func (b *Broker) DuplicateOf(cwd, prompt string) string {
	want := Fingerprint(cwd, prompt)
	b.mu.RLock()
	defer b.mu.RUnlock()
	for id, r := range b.runs {
		switch r.CurrentState() {
		case RunDone, RunFailed, RunCanceled:
			continue
		}
		if Fingerprint(r.CWD, r.Prompt) == want {
			return id
		}
	}
	return ""
}
