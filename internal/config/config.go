// Package config holds the operator's settings for narwhal, in
// ~/.narwhal/config.json, set by hand or with `narwhal config`.
//
// What lives here is what an operator tunes without a rebuild: whether
// each Jev judgement is consulted and the threshold it acts at, the judge's
// timeout, whether scene notes are written, how long a blocking question
// waits. Each was a constant or an environment variable; an environment
// variable cannot be changed under a running daemon, and a constant cannot
// be changed at all.
//
// The file is flat, one key per setting ("jev.answered.threshold": 0.12),
// so what `narwhal config set` takes is what the file says. It is re-read
// when it changes: a setting applies to the next judgement, with no daemon
// restart.
//
// Only a binary reads the file: main calls UseFile. A package's tests that
// never do get the defaults, so no test reads the operator's settings.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is a setting's type.
type Kind string

const (
	Bool     Kind = "bool"
	Float    Kind = "float"
	Duration Kind = "duration"
	String   Kind = "string"
)

// Key is one setting.
type Key struct {
	Name    string
	Kind    Kind
	Default string
	Help    string
}

var (
	mu   sync.Mutex
	keys = map[string]Key{}
	path string
	// The file as last read, and the stat it was read at.
	values  map[string]string
	modTime time.Time
	size    int64
	loadErr error
)

// Register declares a setting. Packages register theirs at init, so the
// CLI lists every setting the binary knows and refuses ones it does not.
func Register(k Key) Key {
	if _, err := parse(k.Kind, k.Default); err != nil {
		panic(fmt.Sprintf("config: default for %s: %v", k.Name, err))
	}
	mu.Lock()
	keys[k.Name] = k
	mu.Unlock()
	return k
}

// Keys returns every registered setting, by name.
func Keys() []Key {
	mu.Lock()
	defer mu.Unlock()
	out := make([]Key, 0, len(keys))
	for _, k := range keys {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Lookup returns the setting called name.
func Lookup(name string) (Key, bool) {
	mu.Lock()
	defer mu.Unlock()
	k, ok := keys[name]
	return k, ok
}

// DefaultPath is ~/.narwhal/config.json, or NARWHAL_CONFIG when set.
func DefaultPath() string {
	if p := strings.TrimSpace(os.Getenv("NARWHAL_CONFIG")); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".narwhal", "config.json")
}

// UseFile makes settings come from the file at p. Until it is called,
// every setting is its default.
func UseFile(p string) {
	mu.Lock()
	path, values, modTime, size, loadErr = p, nil, time.Time{}, 0, nil
	mu.Unlock()
}

// Path is the file in use, or "".
func Path() string {
	mu.Lock()
	defer mu.Unlock()
	return path
}

// refresh re-reads the file when its stat changed. Called under mu.
func refresh() {
	if path == "" {
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		values, modTime, size, loadErr = nil, time.Time{}, 0, nil
		if !os.IsNotExist(err) {
			loadErr = err
		}
		return
	}
	if values != nil && st.ModTime().Equal(modTime) && st.Size() == size {
		return
	}
	modTime, size = st.ModTime(), st.Size()
	vals, err := readFile(path)
	if err != nil {
		// A file being written or hand-edited badly: keep the defaults
		// rather than half of it, and say so where the CLI looks.
		values, loadErr = map[string]string{}, err
		return
	}
	values, loadErr = vals, nil
}

func readFile(p string) (map[string]string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if len(strings.TrimSpace(string(b))) == 0 {
		return map[string]string{}, nil
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		switch v := v.(type) {
		case string:
			out[k] = v
		case bool:
			out[k] = strconv.FormatBool(v)
		case float64:
			out[k] = strconv.FormatFloat(v, 'f', -1, 64)
		default:
			return nil, fmt.Errorf("%s: %s: want a string, number or bool", p, k)
		}
	}
	return out, nil
}

// Err is the error reading the file, if the last read failed.
func Err() error {
	mu.Lock()
	defer mu.Unlock()
	refresh()
	return loadErr
}

// raw is the value set for name and whether the file sets it.
func raw(name string) (Key, string, bool) {
	mu.Lock()
	defer mu.Unlock()
	refresh()
	k, ok := keys[name]
	if !ok {
		panic("config: unregistered key " + name)
	}
	if v, ok := values[name]; ok {
		if _, err := parse(k.Kind, v); err == nil {
			return k, v, true
		}
	}
	return k, k.Default, false
}

// Value is the setting's value as text, and whether the file sets it.
func Value(name string) (string, bool) {
	_, v, set := raw(name)
	return v, set
}

// GetBool is a bool setting.
func GetBool(name string) bool {
	k, v, _ := raw(name)
	x, _ := parse(k.Kind, v)
	b, _ := x.(bool)
	return b
}

// GetFloat is a float setting.
func GetFloat(name string) float64 {
	k, v, _ := raw(name)
	x, _ := parse(k.Kind, v)
	f, _ := x.(float64)
	return f
}

// GetString is a string setting.
func GetString(name string) string {
	_, v, _ := raw(name)
	return strings.TrimSpace(v)
}

// GetDuration is a duration setting.
func GetDuration(name string) time.Duration {
	k, v, _ := raw(name)
	x, _ := parse(k.Kind, v)
	d, _ := x.(time.Duration)
	return d
}

func parse(kind Kind, v string) (any, error) {
	v = strings.TrimSpace(v)
	switch kind {
	case Bool:
		switch strings.ToLower(v) {
		case "true", "on", "yes", "1":
			return true, nil
		case "false", "off", "no", "0":
			return false, nil
		}
		return nil, fmt.Errorf("%q is not on/off", v)
	case Float:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is not a number", v)
		}
		return f, nil
	case String:
		return v, nil
	case Duration:
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return nil, fmt.Errorf("%q is not a duration like 3s or 30m", v)
		}
		return d, nil
	}
	return nil, fmt.Errorf("unknown kind %s", kind)
}

// Set writes name=value to the file, checked against the setting's kind.
func Set(name, value string) error {
	k, ok := Lookup(name)
	if !ok {
		return fmt.Errorf("unknown setting %q (narwhal config lists them)", name)
	}
	x, err := parse(k.Kind, value)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if k.Kind == Float {
		if f := x.(float64); strings.HasSuffix(name, ".threshold") && (f < 0 || f > 1) {
			return fmt.Errorf("%s: a threshold is a probability, between 0 and 1", name)
		}
	}
	return rewrite(func(m map[string]any) {
		switch v := x.(type) {
		case time.Duration:
			m[name] = v.String()
		default:
			m[name] = v
		}
	})
}

// Unset removes name from the file, back to its default.
func Unset(name string) error {
	if _, ok := Lookup(name); !ok {
		return fmt.Errorf("unknown setting %q", name)
	}
	return rewrite(func(m map[string]any) { delete(m, name) })
}

func rewrite(edit func(map[string]any)) error {
	p := Path()
	if p == "" {
		return fmt.Errorf("no config file in use")
	}
	m := map[string]any{}
	if b, err := os.ReadFile(p); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		if err := json.Unmarshal(b, &m); err != nil {
			return fmt.Errorf("%s: %w — fix or remove it first", p, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	edit(m)
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	// Written whole and renamed, so the daemon never reads half a file.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// FormatFloat writes a float the way the file and the CLI show it.
func FormatFloat(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }
