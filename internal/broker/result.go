// result.go is what a task and a run produced, as data rather than prose.
//
// A run's product was text: each task's outcome, and the synthesis task's
// outcome standing in for the run's answer. Everything a person acts on
// afterwards — the branches it pushed, the commits, the PRs to review —
// was inside that text, so nothing could list them, check whether a PR it
// claimed exists, or hand them to the next step. One run on disk names six
// branches, fourteen commits and five PRs, all in a 20,336-character
// outcome.
//
// Artifacts are recorded by the worker as it makes them (the artifact
// script) and, for runs that predate that, read out of the outcome text —
// marked as such, since a hash in prose is a claim, not a record.
package broker

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Artifact kinds a worker can record.
const (
	ArtifactPR     = "pr"
	ArtifactBranch = "branch"
	ArtifactCommit = "commit"
	ArtifactFile   = "file"
)

// Artifacts are the durable things work produced.
type Artifacts struct {
	PRs      []string `json:"prs,omitempty"`
	Branches []string `json:"branches,omitempty"`
	Commits  []string `json:"commits,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// Empty reports whether nothing is recorded.
func (a Artifacts) Empty() bool {
	return len(a.PRs)+len(a.Branches)+len(a.Commits)+len(a.Files) == 0
}

// Count is how many artifacts there are in all.
func (a Artifacts) Count() int {
	return len(a.PRs) + len(a.Branches) + len(a.Commits) + len(a.Files)
}

func (a *Artifacts) list(kind string) (*[]string, error) {
	switch kind {
	case ArtifactPR:
		return &a.PRs, nil
	case ArtifactBranch:
		return &a.Branches, nil
	case ArtifactCommit:
		return &a.Commits, nil
	case ArtifactFile:
		return &a.Files, nil
	}
	return nil, fmt.Errorf("unknown artifact kind %q: want pr, branch, commit or file", kind)
}

// Add records one artifact; recording it twice is a no-op.
func (a *Artifacts) Add(kind, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("empty %s", kind)
	}
	l, err := a.list(kind)
	if err != nil {
		return err
	}
	if !slices.Contains(*l, value) {
		*l = append(*l, value)
	}
	return nil
}

// Merge adds everything in b.
func (a *Artifacts) Merge(b Artifacts) {
	for _, p := range []struct {
		kind string
		vals []string
	}{{ArtifactPR, b.PRs}, {ArtifactBranch, b.Branches}, {ArtifactCommit, b.Commits}, {ArtifactFile, b.Files}} {
		for _, v := range p.vals {
			_ = a.Add(p.kind, v)
		}
	}
}

func (a Artifacts) clone() Artifacts {
	return Artifacts{
		PRs:      append([]string(nil), a.PRs...),
		Branches: append([]string(nil), a.Branches...),
		Commits:  append([]string(nil), a.Commits...),
		Files:    append([]string(nil), a.Files...),
	}
}

// RecordArtifact adds an artifact to the task.
func (t *Task) RecordArtifact(kind, value string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.Artifacts.Add(kind, value)
}

var (
	prURL = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)
	// A hash counts only in backticks and only with both a letter and a
	// digit: bare hex runs in prose are as often sizes and ids.
	backticked = regexp.MustCompile("`([^`\\s]{4,80})`")
	hexHash    = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	branchNear = regexp.MustCompile("(?i)(?:branch|브랜치)[^`\\n]{0,24}`([^`\\s]+)`")
)

// ExtractArtifacts reads artifacts out of prose: PR links, backticked
// commit hashes, and backticked names said to be branches. Files are not
// guessed at — a path in prose is as often one that was read as one that
// was changed.
func ExtractArtifacts(text string) Artifacts {
	var a Artifacts
	for _, u := range prURL.FindAllString(text, -1) {
		_ = a.Add(ArtifactPR, u)
	}
	for _, m := range branchNear.FindAllStringSubmatch(text, -1) {
		if !hexHash.MatchString(m[1]) {
			_ = a.Add(ArtifactBranch, m[1])
		}
	}
	for _, m := range backticked.FindAllStringSubmatch(text, -1) {
		h := m[1]
		if hexHash.MatchString(h) && strings.ContainsAny(h, "abcdef") && strings.ContainsAny(h, "0123456789") {
			_ = a.Add(ArtifactCommit, h)
		}
	}
	return a
}

// RunResult is what a run produced: its answer, and the artifacts of all
// its tasks.
type RunResult struct {
	// Answer is the run's conclusion: the synthesis task's outcome when it
	// has one, else each finished task's outcome under its id.
	Answer string
	// AnswerFrom is the task the answer is, or "" when it was composed.
	AnswerFrom string
	Artifacts  Artifacts
	// Extracted counts artifacts read out of outcome text rather than
	// recorded by a worker.
	Extracted int
}

// Result assembles the run's result from its snapshot.
func (s Snapshot) Result() RunResult {
	var r RunResult
	tasks := append([]TaskSnapshot(nil), s.Tasks...)
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	for _, t := range tasks {
		if t.State == TaskCompleted && isSynthesisName(t.Name, t.Assignment) && strings.TrimSpace(t.Outcome) != "" {
			r.Answer, r.AnswerFrom = strings.TrimSpace(t.Outcome), t.ID
		}
	}
	if r.Answer == "" {
		var parts []string
		for _, t := range tasks {
			if o := strings.TrimSpace(t.Outcome); o != "" && t.State == TaskCompleted {
				parts = append(parts, "## "+t.ID+"\n\n"+o)
			}
		}
		r.Answer = strings.Join(parts, "\n\n")
	}
	for _, t := range tasks {
		if t.Artifacts != nil {
			r.Artifacts.Merge(*t.Artifacts)
		}
	}
	// Read the rest out of the text, and count what that added.
	before := r.Artifacts.Count()
	for _, t := range tasks {
		if t.State == TaskCompleted {
			r.Artifacts.Merge(ExtractArtifacts(t.Outcome))
		}
	}
	r.Extracted = r.Artifacts.Count() - before
	return r
}

// SetLineage records the run this one continues and the flow it belongs
// to. See Snapshot.ParentRun.
func (r *Run) SetLineage(parent, flow string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parentRun, r.flow = parent, flow
}

// FlowID is the flow a snapshot belongs to: its recorded flow, or for a
// run that starts one, itself.
func (s Snapshot) FlowID() string {
	if s.Flow != "" {
		return s.Flow
	}
	return s.RunID
}
