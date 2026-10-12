package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsComeFromTheFileAndFallBack(t *testing.T) {
	Register(Key{Name: "t.on", Kind: Bool, Default: "true"})
	Register(Key{Name: "t.threshold", Kind: Float, Default: "0.3"})
	Register(Key{Name: "t.wait", Kind: Duration, Default: "3s"})
	p := filepath.Join(t.TempDir(), "config.json")
	UseFile(p)
	t.Cleanup(func() { UseFile("") })

	if !GetBool("t.on") || GetFloat("t.threshold") != 0.3 || GetDuration("t.wait") != 3*time.Second {
		t.Fatal("defaults without a file")
	}
	for k, v := range map[string]string{"t.on": "off", "t.threshold": "0.45", "t.wait": "10s"} {
		if err := Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	if GetBool("t.on") || GetFloat("t.threshold") != 0.45 || GetDuration("t.wait") != 10*time.Second {
		t.Fatal("set values not read back")
	}
	if err := Set("t.threshold", "1.5"); err == nil {
		t.Error("a threshold above 1 was accepted")
	}
	if err := Set("t.wait", "soon"); err == nil {
		t.Error("a bad duration was accepted")
	}
	if err := Set("nope", "1"); err == nil {
		t.Error("an unknown key was accepted")
	}
	if err := Unset("t.threshold"); err != nil || GetFloat("t.threshold") != 0.3 {
		t.Errorf("unset: %v, %v", err, GetFloat("t.threshold"))
	}

	// A hand edit is picked up; a broken file keeps the defaults and says so.
	os.WriteFile(p, []byte(`{"t.threshold": 0.7, "t.extra": 1}`), 0o644)
	future := time.Now().Add(time.Second)
	os.Chtimes(p, future, future)
	if GetFloat("t.threshold") != 0.7 {
		t.Errorf("hand edit not read: %v", GetFloat("t.threshold"))
	}
	os.WriteFile(p, []byte(`{"t.threshold": `), 0o644)
	os.Chtimes(p, future.Add(time.Second), future.Add(time.Second))
	if GetFloat("t.threshold") != 0.3 || Err() == nil {
		t.Errorf("broken file: %v, err %v", GetFloat("t.threshold"), Err())
	}
}

// Without UseFile, nothing is read: tests of other packages get defaults.
func TestNoFileMeansDefaults(t *testing.T) {
	Register(Key{Name: "t.d", Kind: Float, Default: "0.5"})
	UseFile("")
	if GetFloat("t.d") != 0.5 {
		t.Error("default")
	}
}
