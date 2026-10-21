package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func spawnStatus(t *testing.T, addr string, body map[string]any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(addr+"/api/v1/control/spawn", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestSpawnRecordsTheRunsPermissions(t *testing.T) {
	addr := spawnFixture(t)
	perms := map[string]any{"mode": "dontAsk", "allow": []string{"Read"}}
	out := postSpawn(t, addr, map[string]any{
		"cwd":         t.TempDir(),
		"permissions": perms,
		"workers":     []map[string]any{{"name": "logs", "assignment": "read the logs"}},
	})
	runID, _ := out["run_id"].(string)
	snap, _ := getJSON(t, addr+"/api/v1/control/status?run_id="+runID)["snapshot"].(map[string]any)
	got, _ := snap["permissions"].(map[string]any)
	if got["mode"] != "dontAsk" {
		t.Fatalf("snapshot permissions = %v", snap["permissions"])
	}

	// Adding workers with the same policy is fine; changing it is not.
	code, _ := spawnStatus(t, addr, map[string]any{
		"run_id": runID, "cwd": t.TempDir(), "permissions": perms,
		"workers": []map[string]any{{"name": "metrics", "assignment": "read metrics"}},
	})
	if code != http.StatusOK {
		t.Errorf("same policy on an existing run: %d", code)
	}
	code, out = spawnStatus(t, addr, map[string]any{
		"run_id": runID, "cwd": t.TempDir(), "permissions": map[string]any{"mode": "bypassPermissions"},
		"workers": []map[string]any{{"name": "fix", "assignment": "fix it"}},
	})
	if code != http.StatusConflict {
		t.Errorf("widening an existing run's policy: %d %v", code, out)
	}
}

func TestSpawnRejectsAPolicyItCannotHonour(t *testing.T) {
	addr := spawnFixture(t)
	code, out := spawnStatus(t, addr, map[string]any{
		"cwd":         t.TempDir(),
		"permissions": map[string]any{"allow": []string{"Read"}},
		"workers":     []map[string]any{{"assignment": "x"}},
	})
	if code != http.StatusBadRequest {
		t.Errorf("allow list without dontAsk: %d %v", code, out)
	}
}
