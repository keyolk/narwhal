package launcher

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"testing"
)

// Workers call ask positionally, as the usage says, and with --assume and
// --blocking; the flag itself must never become the assumption.
func TestAskScriptReadsPositionalAndFlagForms(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		got = append(got, m)
		w.Write([]byte(`{"id":1,"status":"recorded"}`))
	}))
	defer srv.Close()
	script := filepath.Join(agentScripts(t, srv.URL), "scripts", "ask")
	for _, args := range [][]string{
		{"Which format?", "json", "--option", "json", "--option", "yaml", "--recommend", "1"},
		{"Which format?", "--assume", "json", "--option", "json", "--option", "yaml"},
		{"Which branch?", "main", "blocking"},
		{"Which branch?", "--blocking", "--assume", "main"},
	} {
		if out, err := exec.Command("bash", append([]string{script}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v %s", args, err, out)
		}
	}
	want := []struct {
		q, assumed string
		blocking   bool
		options    int
	}{{"Which format?", "json", false, 2}, {"Which format?", "json", false, 2},
		{"Which branch?", "main", true, 0}, {"Which branch?", "main", true, 0}}
	for i, w := range want {
		g := got[i]
		opts, _ := g["options"].([]any)
		if g["question"] != w.q || g["assumed"] != w.assumed || g["blocking"] != w.blocking || len(opts) != w.options {
			t.Errorf("call %d = %+v, want %+v", i, g, w)
		}
	}
}
