package main

import "testing"

// isolateTerminalEnv clears every variable icon detection reads.
//
// Without this the test inherits the developer's real terminal: this
// machine exports GHOSTTY_RESOURCES_DIR, so a "plain terminal" case passes
// on CI's clean environment and fails locally. Detection grew to read more
// variables than the original tests cleared, which is exactly how a
// non-hermetic test appears.
func isolateTerminalEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"NARWHAL_ICONS", "NARWHAL_FONT", "TERM_FONT",
		"TERM_PROGRAM", "TERM",
		"GHOSTTY_RESOURCES_DIR", "GHOSTTY_BIN_DIR", "WEZTERM_EXECUTABLE",
	} {
		t.Setenv(key, "")
	}
}

func TestNerdFontDetectedThroughTmux(t *testing.T) {
	// The regression this exists for: inside tmux TERM_PROGRAM reads
	// "tmux", which hides the terminal actually drawing the pixels. A user
	// running Ghostty with Hack Nerd Font Mono got the fallback glyphs for
	// exactly as long as they stayed in tmux — which is always.
	isolateTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "tmux")
	t.Setenv("TERM", "tmux-256color")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/Applications/Ghostty.app/Contents/Resources/ghostty")

	if !hasNerdFont() {
		t.Error("Ghostty's marker variable did not survive the tmux disguise")
	}
}

func TestNerdFontDetectedFromTerm(t *testing.T) {
	// TERM is set per-terminal on the outer side and carries the name
	// through: ghostty sets xterm-ghostty.
	isolateTerminalEnv(t)
	if !isNerdFontTerminal("", "xterm-ghostty") {
		t.Error("xterm-ghostty was not recognized")
	}
	if !isNerdFontTerminal("", "wezterm") {
		t.Error("wezterm TERM was not recognized")
	}
}

func TestNerdFontStillDetectedDirectly(t *testing.T) {
	isolateTerminalEnv(t)
	if !isNerdFontTerminal("ghostty", "xterm-256color") {
		t.Error("a direct TERM_PROGRAM match regressed")
	}
}

func TestPlainTerminalGetsPortableGlyphs(t *testing.T) {
	// Guessing wrong toward Nerd Font fills the pane with tofu, so an
	// unrecognized terminal must stay on the portable set.
	isolateTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("TERM", "xterm-256color")

	if hasNerdFont() {
		t.Error("an unrecognized terminal was assumed to have a Nerd Font")
	}
}

func TestExplicitOverrideWins(t *testing.T) {
	isolateTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "Apple_Terminal")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NARWHAL_ICONS", "nerd")
	if got := resolveIcons(); got.taskCompleted != nerdIcons.taskCompleted {
		t.Error("NARWHAL_ICONS=nerd did not force the nerd set")
	}

	t.Setenv("NARWHAL_ICONS", "unicode")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/somewhere")
	if got := resolveIcons(); got.taskCompleted != unicodeIcons.taskCompleted {
		t.Error("NARWHAL_ICONS=unicode did not force the portable set")
	}
}

func TestEveryIconSetIsComplete(t *testing.T) {
	// A missing glyph renders as an empty column, which silently
	// misaligns the row it is in rather than failing loudly.
	for name, set := range map[string]iconSet{"nerd": nerdIcons, "unicode": unicodeIcons} {
		fields := map[string]string{
			"taskCompleted": set.taskCompleted, "taskDispatched": set.taskDispatched,
			"taskReady": set.taskReady, "taskFailed": set.taskFailed,
			"taskBlocked": set.taskBlocked, "taskPending": set.taskPending,
			"prioUrgent": set.prioUrgent, "prioFYI": set.prioFYI, "prioNormal": set.prioNormal,
			"runActive": set.runActive, "runDone": set.runDone,
			"runFailed": set.runFailed, "runCanceled": set.runCanceled,
			"fieldModel": set.fieldModel, "fieldDeps": set.fieldDeps,
			"fieldBlocks": set.fieldBlocks, "fieldDispatch": set.fieldDispatch,
			"fieldActivity": set.fieldActivity, "fieldFiles": set.fieldFiles,
		}
		for field, glyph := range fields {
			if glyph == "" {
				t.Errorf("%s icon set has no %s glyph", name, field)
			}
		}
	}
}

func TestIconSetOverride(t *testing.T) {
	isolateTerminalEnv(t)
	t.Setenv("NARWHAL_ICONS", "nerd")
	if got := resolveIcons(); got != nerdIcons {
		t.Fatal("NARWHAL_ICONS=nerd should select the Nerd Font set")
	}
	t.Setenv("NARWHAL_ICONS", "unicode")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("NARWHAL_ICONS=unicode should select the portable set")
	}
	// An explicit override must win over terminal detection.
	t.Setenv("TERM_PROGRAM", "ghostty")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("explicit override should beat terminal detection")
	}
}

func TestIconSetDefaultsToUnicode(t *testing.T) {
	// Guessing wrong toward Nerd Font fills the pane with tofu, so an
	// unknown terminal must fall back to portable glyphs.
	isolateTerminalEnv(t)
	t.Setenv("TERM_PROGRAM", "some-unknown-terminal")
	if got := resolveIcons(); got != unicodeIcons {
		t.Fatal("unknown terminal should default to the portable set")
	}
}

func TestNerdFontDetectedFromFontName(t *testing.T) {
	isolateTerminalEnv(t)
	t.Setenv("NARWHAL_FONT", "JetBrainsMono Nerd Font")
	if got := resolveIcons(); got != nerdIcons {
		t.Fatal("a font name containing 'nerd' should select the Nerd Font set")
	}
}
