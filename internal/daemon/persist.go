// persist.go writes daemon run snapshots to disk.
//
// The batch CLI saves a run when its coordinator returns. The daemon had no
// equivalent, so an interactive run — which is how nearly every run happens
// — left nothing behind: `narwhal show` could not see it, and if the daemon
// died the whole record went with it.
//
// That is not hypothetical. A run of four workers finished real work, its
// files landed in the target repo, and the daemon was restarted underneath
// it. Nothing on disk showed the run had ever existed.
package daemon

import (
	"log"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/store"
)

// runFingerprint is the part of a run whose change is worth a write.
//
// Snapshots are saved from the dispatch tick, which runs twice a second.
// Writing every tick would be pointless disk traffic for a graph that only
// changes when a worker finishes or a message lands, so the fingerprint
// gates it.
type runFingerprint struct {
	states   string // task id + state, concatenated
	messages int
	tasks    int
}

func fingerprintOf(snap broker.Snapshot) runFingerprint {
	var b []byte
	for _, t := range snap.Tasks {
		b = append(b, t.ID...)
		b = append(b, '=')
		b = append(b, t.State...)
		b = append(b, ';')
	}
	return runFingerprint{
		states:   string(b),
		messages: len(snap.Messages),
		tasks:    len(snap.Tasks),
	}
}

// persistRun writes a run's snapshot if anything meaningful changed since
// the last write. Returns whether it wrote.
func (d *Dispatcher) persistRun(runID string, run *broker.Run) bool {
	snap := run.Snapshot()
	fp := fingerprintOf(snap)

	d.mu.Lock()
	if d.saved == nil {
		d.saved = make(map[string]runFingerprint)
	}
	prev, seen := d.saved[runID]
	if seen && prev == fp {
		d.mu.Unlock()
		return false
	}
	d.saved[runID] = fp
	d.mu.Unlock()

	if err := store.SaveRun(snap); err != nil {
		log.Printf("[dispatch] save run %s: %v", runID, err)
		return false
	}
	return true
}

// PersistAll writes every live run, ignoring the change fingerprint.
//
// Called on shutdown, where the point is to get the final state down
// regardless of whether the last tick already wrote it.
func (d *Dispatcher) PersistAll() int {
	n := 0
	for _, runID := range d.sess.ActiveRuns() {
		run := d.sess.Broker.GetRun(runID)
		if run == nil {
			continue
		}
		if err := store.SaveRun(run.Snapshot()); err != nil {
			log.Printf("[dispatch] save run %s: %v", runID, err)
			continue
		}
		n++
	}
	return n
}
