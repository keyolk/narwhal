package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
