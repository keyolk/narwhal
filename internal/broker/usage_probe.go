// usage_probe.go attaches measured cost to a task when it stops running.
//
// The probe is injected rather than called by each dispatcher, for the
// reason #36 established: a rule only one dispatcher knows is a rule that
// silently does not apply to the other. A task reaches a terminal state
// down three paths — completed, failed by the circuit breaker, cancelled
// with the run — and accounting that only covered the first would report
// a cancelled run as free, which is the opposite of the truth.
//
// It is an interface and not a direct call into internal/usage because
// the broker must stay testable without a Claude transcript on disk, and
// because reading a transcript is one way to learn a task's cost rather
// than the definition of it.
package broker

// UsageProbe measures what a task's worker consumed. Implementations read
// whatever artifact records that; the broker only needs the number.
//
// Returning a nil *Usage means "not measurable", which the snapshot keeps
// distinct from a measured zero — an unmeasured task must not read as a
// free one.
type UsageProbe interface {
	TaskUsage(runID, taskID string) *Usage
}

// UsageProbeFunc adapts a function to UsageProbe.
type UsageProbeFunc func(runID, taskID string) *Usage

func (f UsageProbeFunc) TaskUsage(runID, taskID string) *Usage { return f(runID, taskID) }

// SetUsageProbe installs the probe used to measure tasks in this run.
// Nil disables accounting, which is what every test that does not care
// about cost gets by default.
func (r *Run) SetUsageProbe(p UsageProbe) {
	r.mu.Lock()
	r.usageProbe = p
	r.mu.Unlock()
}

// measureTask records a task's cost, if a probe is installed and the task
// does not already carry one.
//
// Already-measured tasks are left alone so a retry cannot overwrite the
// accounting of the attempt before it — but the probe reads a transcript
// that a retry appends to under the same session id, so what it returns
// already covers every attempt. Re-reading would be correct and merely
// wasteful; the guard is here because the reverse — a cheap final attempt
// erasing an expensive one — is the failure that would go unnoticed.
func (r *Run) measureTask(taskID string) {
	r.mu.RLock()
	probe := r.usageProbe
	r.mu.RUnlock()
	if probe == nil {
		return
	}
	u := probe.TaskUsage(r.ID, taskID)
	if u == nil {
		return
	}
	r.mu.RLock()
	t := r.Tasks[taskID]
	r.mu.RUnlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.Usage == nil {
		t.Usage = u
	}
	t.mu.Unlock()
}

// RunUsage totals what a run consumed, and says how much of it is
// actually measured.
//
// The second return is not decoration. A run whose transcripts were
// removed reports a small total for the same reason a cheap run does, and
// a caller that cannot tell those apart will publish the small number.
// measured counts tasks carrying a tally; unmeasured counts tasks that
// ran and do not.
func (r *Run) RunUsage() (total Usage, measured, unmeasured int) {
	for _, ts := range r.SnapshotTasks() {
		if ts.Usage == nil {
			if ts.Dispatches > 0 {
				unmeasured++
			}
			continue
		}
		measured++
		total.InputTokens += ts.Usage.InputTokens
		total.OutputTokens += ts.Usage.OutputTokens
		total.CacheCreationTokens += ts.Usage.CacheCreationTokens
		total.CacheReadTokens += ts.Usage.CacheReadTokens
		total.Turns += ts.Usage.Turns
	}
	return total, measured, unmeasured
}
