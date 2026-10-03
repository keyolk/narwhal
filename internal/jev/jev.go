// Package jev is a client for Jev, a decision model that answers a typed
// question about a piece of state with a probability instead of prose.
//
// That shape is why it fits a dispatcher. A frontier model asked "did this
// worker finish?" writes a paragraph that then has to be parsed, and can be
// talked into an answer by the text it is judging. Jev returns a number,
// so there is nothing to parse and nothing to inject into. It is also fast
// enough to sit on the reap path: p50 ≈ 600ms from this machine, against a
// worker run measured in minutes.
//
// Every failure is reported as "undecided" (ok=false), never as an error
// the caller must handle. Jev is advice: a caller keeps whatever it would
// have done without it, so a gateway outage cannot change what narwhal does
// — only how well-informed it is.
package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	Endpoint = "https://ai-gateway.vercel.sh/v1/evaluate"
	Model    = "typesafe-ai/jev"

	// PassEntry is where the gateway key lives when NARWHAL_JEV_KEY is
	// unset. It is the same entry ~/.claude/hooks/jev.py reads, so one key
	// rotation covers both.
	PassEntry = "sendbird/vercel/ai-gateway/api-key"

	DefaultTimeout = 3 * time.Second
)

// Client asks Jev questions. The zero value is not usable; use New.
type Client struct {
	http    *http.Client
	keyFn   func() (string, error)
	keyOnce sync.Once
	key     string
	keyErr  error
	url     string
}

// New returns a client that reads its key from NARWHAL_JEV_KEY, falling
// back to `pass show PassEntry`. The key is resolved on first use, not
// here, so constructing a client on a machine without a key costs nothing
// and fails only as "undecided".
func New() *Client {
	return &Client{
		http:  &http.Client{Timeout: DefaultTimeout},
		keyFn: defaultKey,
		url:   Endpoint,
	}
}

// NewWithKey is for tests and callers that manage the key themselves.
func NewWithKey(url, key string, timeout time.Duration) *Client {
	return &Client{
		http:  &http.Client{Timeout: timeout},
		keyFn: func() (string, error) { return key, nil },
		url:   url,
	}
}

func defaultKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("NARWHAL_JEV_KEY")); k != "" {
		return k, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "pass", "show", PassEntry)
	// The daemon has no terminal. A locked gpg-agent would otherwise raise
	// a pinentry window from a background process, or block until the
	// timeout; failing fast leaves the judge undecided, which is harmless.
	cmd.Env = append(os.Environ(), "PASSWORD_STORE_GPG_OPTS=--pinentry-mode=error")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pass show %s: %w", PassEntry, err)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	if line = strings.TrimSpace(line); line == "" {
		return "", fmt.Errorf("pass show %s: empty", PassEntry)
	}
	return line, nil
}

func (c *Client) apiKey() (string, error) {
	c.keyOnce.Do(func() { c.key, c.keyErr = c.keyFn() })
	return c.key, c.keyErr
}

type question struct {
	Type         string `json:"type"`
	Instructions string `json:"instructions"`
}

type answer struct {
	Probability *float64 `json:"probability"`
}

func (c *Client) evaluate(ctx context.Context, state, name string, q question) (answer, bool) {
	key, err := c.apiKey()
	if err != nil {
		return answer{}, false
	}
	body, err := json.Marshal(map[string]any{
		"model":     Model,
		"state":     state,
		"questions": map[string]question{name: q},
	})
	if err != nil {
		return answer{}, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return answer{}, false
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return answer{}, false
	}
	var payload struct {
		Answers map[string]answer `json:"answers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return answer{}, false
	}
	a, ok := payload.Answers[name]
	return a, ok
}

// Ask returns the probability that instructions hold for state. ok=false
// means undecided — no key, no network, a slow gateway, a malformed reply.
func (c *Client) Ask(ctx context.Context, state, name, instructions string) (p float64, ok bool) {
	a, ok := c.evaluate(ctx, state, name, question{Type: "boolean", Instructions: instructions})
	if !ok || a.Probability == nil {
		return 0, false
	}
	return *a.Probability, true
}
