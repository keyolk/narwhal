// Package broker identity helpers.
//
// Each agent in a Run gets an opaque token that doubles as its broker
// endpoint path segment. The broker derives sender identity from the URL
// (which token was used) rather than trusting a field in the message body,
// mirroring AgentRadio's CORAL_CONNECTION_URL approach: the model never
// handles the URL, and the endpoint secret IS the identity.
package broker

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
)

// AgentRegistry tracks the agents enrolled in a Run and their tokens.
type AgentRegistry struct {
	mu     sync.RWMutex
	agents map[string]*Agent // token → agent
}

// Agent is one participant in a Run's radio channel.
type Agent struct {
	ID        string // logical name, e.g. "worker-1", "main"
	Token     string // opaque endpoint secret
	RunID     string
	IsCoordinator bool
}

// NewAgentRegistry returns an empty registry.
func NewAgentRegistry() *AgentRegistry {
	return &AgentRegistry{agents: make(map[string]*Agent)}
}

// Register creates an agent with a fresh random token and returns it.
// The token is what gets baked into the agent's wrapper scripts so the
// model never sees the URL or the token directly.
func (ar *AgentRegistry) Register(agentID, runID string, isCoordinator bool) *Agent {
	token := generateToken()
	a := &Agent{
		ID:           agentID,
		Token:        token,
		RunID:        runID,
		IsCoordinator: isCoordinator,
	}
	ar.mu.Lock()
	ar.agents[token] = a
	ar.mu.Unlock()
	return a
}

// LookupByToken resolves an agent from its endpoint token. Returns nil if
// the token is unknown.
func (ar *AgentRegistry) LookupByToken(token string) *Agent {
	ar.mu.RLock()
	defer ar.mu.RUnlock()
	return ar.agents[token]
}

// Agents returns all registered agents (for snapshots/viewer).
func (ar *AgentRegistry) Agents() []*Agent {
	ar.mu.RLock()
	defer ar.mu.RUnlock()
	out := make([]*Agent, 0, len(ar.agents))
	for _, a := range ar.agents {
		out = append(out, a)
	}
	return out
}

// generateToken returns 16 random bytes hex-encoded (32 chars), sufficient
// as an unguessable local endpoint path segment.
func generateToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing means the host is in serious trouble;
		// fall back to a non-cryptographic but still unique value.
		return fmt.Sprintf("fallback-%x", b)
	}
	return hex.EncodeToString(b)
}
