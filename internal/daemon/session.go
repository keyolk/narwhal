// session.go holds the daemon's live state: the broker, the agent registry,
// and the launchers for every run currently in flight.
//
// A single interactive Claude Code session can have several runs open at
// once (the user asks one thing, then another while the first is still
// working), so the daemon keys launchers by run id rather than assuming a
// single active run the way the batch CLI does.
package daemon

import (
	"fmt"
	"sync"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
	"github.com/keyolk/narwhal/internal/launcher"
)

// Session is the daemon's owned state.
type Session struct {
	Broker   *broker.Broker
	Registry *broker.AgentRegistry
	URL      string

	mu        sync.Mutex
	launchers map[string]*launcher.Launcher // runID → launcher
	seq       int                           // monotonic id source for runs/tasks
}

// NewSession creates empty daemon state. URL is filled in once the HTTP
// server binds.
func NewSession() *Session {
	return &Session{
		Broker:    broker.New(),
		Registry:  broker.NewAgentRegistry(),
		launchers: make(map[string]*launcher.Launcher),
	}
}

// NextID returns a process-unique suffix for generated run and task ids.
func (s *Session) NextID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// LauncherFor returns the launcher for a run, creating it on first use.
// cwd is only honoured when the launcher is created; later calls reuse the
// existing one so all workers in a run share a session directory.
func (s *Session) LauncherFor(runID, cwd string) *launcher.Launcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	if l, ok := s.launchers[runID]; ok {
		return l
	}
	l := launcher.New(s.URL, runID, cwd)
	s.launchers[runID] = l
	return l
}

// Launcher returns the existing launcher for a run, or nil.
func (s *Session) Launcher(runID string) *launcher.Launcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.launchers[runID]
}

// DropLauncher forgets a run's launcher once the run is finished.
func (s *Session) DropLauncher(runID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.launchers, runID)
}

// ActiveRuns returns run ids that still have a launcher registered.
func (s *Session) ActiveRuns() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.launchers))
	for id := range s.launchers {
		out = append(out, id)
	}
	return out
}

// NewRunID mints a run id. Interactive runs are keyed by wall clock plus a
// process-local counter so two spawns in the same millisecond cannot collide.
func (s *Session) NewRunID() string {
	return fmt.Sprintf("s%d-%d", time.Now().UnixNano()/1e6, s.NextID())
}
