package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/config"
)

func TestAskSendsTheQuestionAndReadsTheProbability(t *testing.T) {
	var got struct {
		Model     string              `json:"model"`
		State     string              `json:"state"`
		Questions map[string]question `json:"questions"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Write([]byte(`{"answers":{"done":{"type":"boolean","probability":0.83}}}`))
	}))
	defer srv.Close()

	p, ok := NewWithKey(srv.URL, "k", time.Second).Ask(context.Background(), "s", "done", "is it?")
	if !ok || p != 0.83 {
		t.Fatalf("Ask = %v, %v; want 0.83, true", p, ok)
	}
	if got.Model != Model || got.State != "s" || got.Questions["done"].Instructions != "is it?" ||
		got.Questions["done"].Type != "boolean" {
		t.Errorf("request = %+v", got)
	}
}

// Every failure is "undecided". A caller that saw an error here would have
// to decide what an outage means, and every caller deciding differently is
// how an outage changes behaviour.
func TestEveryFailureIsUndecided(t *testing.T) {
	cases := map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("<html>")) },
		"wrong question": func(w http.ResponseWriter, _ *http.Request) {
			w.Write([]byte(`{"answers":{"other":{"probability":1}}}`))
		},
		"no probability": func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"answers":{"done":{}}}`)) },
		"slow": func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(time.Second):
			case <-r.Context().Done():
			}
		},
	}
	for name, h := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			_, ok := NewWithKey(srv.URL, "k", 100*time.Millisecond).Ask(context.Background(), "s", "done", "q")
			if ok {
				t.Error("got a decision from a failed call")
			}
		})
	}
}

func TestMissingKeyIsUndecidedWithoutTouchingTheNetwork(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	c := NewWithKey(srv.URL, "", time.Second)
	c.keyFn = func() (string, error) { return "", context.Canceled }

	if _, ok := c.Ask(context.Background(), "s", "done", "q"); ok {
		t.Error("decided without a key")
	}
	if hit {
		t.Error("called the gateway without a key")
	}
}

// Where the key lives is the person's to say: with no jev.pass_entry, pass(1)
// is not run at all; with one, that entry is what is read.
func TestTheKeyComesFromTheConfiguredPassEntry(t *testing.T) {
	t.Setenv("NARWHAL_JEV_KEY", "")
	bin := t.TempDir()
	log := filepath.Join(bin, "called")
	script := "#!/bin/sh\necho \"$@\" > " + log + "\necho secret-key\n"
	if err := os.WriteFile(filepath.Join(bin, "pass"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	cfg := filepath.Join(t.TempDir(), "config.json")
	config.UseFile(cfg)
	t.Cleanup(func() { config.UseFile("") })

	if _, err := defaultKey(); err == nil {
		t.Fatal("found a key with no pass entry configured")
	}
	if _, err := os.Stat(log); err == nil {
		t.Fatal("ran pass with no entry configured")
	}
	if err := config.Set("jev.pass_entry", "me/gateway-key"); err != nil {
		t.Fatal(err)
	}
	key, err := defaultKey()
	if err != nil || key != "secret-key" {
		t.Fatalf("key = %q, %v", key, err)
	}
	if b, _ := os.ReadFile(log); strings.TrimSpace(string(b)) != "show me/gateway-key" {
		t.Errorf("pass called with %q", b)
	}
}
