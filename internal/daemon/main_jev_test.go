package daemon

import (
	"os"
	"testing"
)

// Tests must not reach the Jev gateway or shell out to pass: a worker that
// exits without task-done is judged, and these tests exit workers that way.
func TestMain(m *testing.M) {
	os.Setenv("NARWHAL_JEV", "off")
	os.Exit(m.Run())
}
