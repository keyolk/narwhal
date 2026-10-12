package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/keyolk/narwhal/internal/broker"
)

// withOutputs gives the fixture what the plan run on disk had: a plan file
// a task recorded, and a report posted to the results thread instead of
// the outcome.
func withOutputs(t *testing.T) tuiModel {
	t.Helper()
	m := fixtureRun(t)
	must(t, os.WriteFile(filepath.Join(m.snap.CWD, "PLAN.md"), []byte("# Lock plan\n\n- step one\n- step two\n"), 0o644))
	m.snap.Tasks[0].Artifacts = &broker.Artifacts{Files: []string{"PLAN.md"}}
	m.snap.Messages = append(m.snap.Messages, &broker.Message{Seq: 7, Sender: "worker-client",
		ThreadID: broker.ResultsThread, Content: "## Client findings\n\nthree mappers folded into one"})
	return m
}

func TestRunOutputsListsFilesReportsThenAnswers(t *testing.T) {
	m := withOutputs(t)
	outs := runOutputs(m.snap)
	var got []string
	for _, o := range outs {
		got = append(got, outputLabels[o.kind]+":"+o.task)
	}
	want := []string{"file:schema", "report:client", "answer:client", "answer:schema"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
	if outs[0].path != filepath.Join(m.snap.CWD, "PLAN.md") {
		t.Errorf("file path = %q, want it resolved against the run's directory", outs[0].path)
	}
	if !strings.HasPrefix(outs[1].title, "Client findings") {
		t.Errorf("report title = %q", outs[1].title)
	}
}

func TestOutputsViewShowsTheSelectedOutputRendered(t *testing.T) {
	m := press(withOutputs(t), "o")
	if !m.outputsView {
		t.Fatal("o on the run view did not open Outputs")
	}
	view := ansi.Strip(m.View())
	for _, want := range []string{"Outputs", "PLAN.md", "Lock plan", "step two", "Client findings"} {
		if !strings.Contains(view, want) {
			t.Errorf("Outputs view lacks %q:\n%s", want, view)
		}
	}
	m = press(m, "j")
	if view := ansi.Strip(m.View()); !strings.Contains(view, "three mappers folded into one") {
		t.Errorf("j did not show the report:\n%s", view)
	}
	m = press(m, "esc")
	if m.outputsView {
		t.Error("esc did not leave Outputs")
	}
}

func TestOutputsFromATaskStartsAtItsOutput(t *testing.T) {
	m := withOutputs(t)
	m.openOutputs("client")
	outs := runOutputs(m.snap)
	if outs[m.outputSel].task != "client" {
		t.Fatalf("selected %s's output, want client's", outs[m.outputSel].task)
	}
}

func TestOutputsViewDrawsAtAnySize(t *testing.T) {
	m := withOutputs(t)
	m.outputsView = true
	for w := 0; w <= 12; w++ {
		for h := 0; h <= 12; h++ {
			m.width, m.height = w, h
			_ = m.View()
		}
	}
}

func TestPageOutputReportsAMissingFile(t *testing.T) {
	cmd := pageOutput(output{kind: outFile, path: filepath.Join(t.TempDir(), "gone.md")})
	msg, ok := cmd().(noticeMsg)
	if !ok || msg.err == nil {
		t.Fatalf("paging a missing file gave %#v, want an error notice", msg)
	}
}
