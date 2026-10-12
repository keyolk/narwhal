package daemon

import (
	"os"
	"testing"
)

// Tests must not reach the Jev gateway or shell out to pass: a worker that
// exits without task-done is judged, and these tests exit workers that way.
// Nor start a model to summarise the scenes the dispatch tick sees.
func TestMain(m *testing.M) {
	os.Setenv("NARWHAL_JEV", "off")
	os.Setenv("NARWHAL_SCENE_NOTES", "off")
	os.Exit(m.Run())
}
