package main

import (
	"strings"
	"testing"
	"time"

	"github.com/keyolk/narwhal/internal/broker"
)

// The monitor had 38 uses of the dim style against 10 of every colour
// combined, so almost everything on screen was grey and nothing stood out
// from anything else. Colour now carries meaning — the same hue means the
// same thing in every pane — and these tests pin the parts where a wrong
// colour would mislead rather than merely look flat.
//
// Every test here forces a colour profile first. Without a TTY lipgloss
// renders plain text, so a "was this styled?" assertion passes on nothing.

// ansiFor returns the escape sequence a style emits, for comparing what a
// fragment was rendered with.
func ansiFor(s string) string {
	if i := strings.Index(s, "\x1b["); i >= 0 {
		if j := strings.Index(s[i:], "m"); j >= 0 {
			return s[i : i+j+1]
		}
	}
	return ""
}

func colourModel(t *testing.T) tuiModel {
	t.Helper()
	forceColor(t)
	t.Setenv("HOME", t.TempDir())
	m := testModel(0, 0)
	m.width, m.height = 120, 24
	m.focus = focusTasks
	m.snap.RunID = "r1"
	m.snap.State = broker.RunActive
	return m
}

func TestFooterColoursOnlyNonZeroCounts(t *testing.T) {
	// A count of zero is not news. Colouring it too would make the footer
	// four bright numbers where only one of them matters.
	m := colourModel(t)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskCompleted},
		{ID: "b", State: broker.TaskDispatched},
	}

	footer := m.viewFooter()
	line := strings.Split(footer, "\n")[0]

	if !strings.Contains(line, "\x1b[32m") && !strings.Contains(line, "\x1b[1;32m") {
		t.Errorf("a non-zero done count is not green: %q", line)
	}
	// "failed 0" must be dim, i.e. rendered inside a faint sequence.
	idx := strings.Index(line, "failed")
	if idx < 0 {
		t.Fatalf("no failed count: %q", line)
	}
	if !strings.Contains(line[:idx], "\x1b[2m") {
		t.Errorf("a zero failed count was not dimmed: %q", line)
	}
}

func TestUrgentMessagesStandOut(t *testing.T) {
	// An urgent message may invalidate what a peer is doing right now.
	// That is the one kind of traffic worth pulling the eye across a pane.
	m := colourModel(t)
	urgent := &broker.Message{Seq: 1, Sender: "worker-a", Priority: broker.PriorityUrgent,
		Content: "the provider lock releases early", CreatedAt: time.Now()}
	normal := &broker.Message{Seq: 2, Sender: "worker-a", Priority: broker.PriorityNormal,
		Content: "the provider lock releases early", CreatedAt: time.Now()}

	u := m.radioRow(urgent, 100, false)
	n := m.radioRow(normal, 100, false)
	if u == n {
		t.Fatal("urgent and normal messages render identically")
	}
	// Check the body, not the row: the priority glyph is already red, so a
	// row-wide search would pass even with the text left unstyled — which
	// is exactly what it did before this was narrowed.
	body := func(row string) string {
		i := strings.Index(row, "provider")
		if i < 0 {
			t.Fatalf("row does not contain the message: %q", row)
		}
		return row[:i]
	}
	if !strings.Contains(body(u), "\x1b[1;31m") {
		t.Errorf("an urgent message body is not bold red: %q", u)
	}
	if strings.Contains(body(n), "\x1b[1;31m") {
		t.Errorf("a normal message body was styled as urgent: %q", n)
	}
}

func TestProtocolTrafficRecedes(t *testing.T) {
	// Coordination is context for the prose around it. If it competed with
	// a worker's actual finding the channel would read as noise.
	m := colourModel(t)
	claim := &broker.Message{Seq: 1, Sender: "worker-a", Priority: broker.PriorityNormal,
		Content: broker.FormatFileClaimRequest(broker.FileClaimPrefix, "a",
			[]string{"internal/api/router.go"}), CreatedAt: time.Now()}
	prose := &broker.Message{Seq: 2, Sender: "worker-a", Priority: broker.PriorityNormal,
		Content: "internal/api/router.go rewrites the retry loop", CreatedAt: time.Now()}

	if !strings.Contains(m.radioRow(claim, 100, false), "\x1b[2m") {
		t.Error("protocol traffic is not dimmed")
	}
	// The prose must not be dimmed into the same weight.
	row := m.radioRow(prose, 100, false)
	body := row[strings.Index(row, "rewrites"):]
	if strings.Contains(body, "\x1b[2m") {
		t.Errorf("a worker's finding was dimmed like protocol traffic: %q", row)
	}
}

func TestMentionsAreMarked(t *testing.T) {
	// A mention says the message is addressed at someone, which changes
	// how the rest of it reads.
	m := colourModel(t)
	msg := &broker.Message{Seq: 1, Sender: "worker-a", Priority: broker.PriorityNormal,
		Mentions: []string{"task-2"}, Content: "look at this", CreatedAt: time.Now()}

	row := m.radioRow(msg, 100, false)
	if !strings.Contains(row, "\x1b[35m") {
		t.Errorf("a mention is not marked: %q", row)
	}
}

func TestFocusedPaneTitleIsColoured(t *testing.T) {
	// The focused pane is where the keys go. Bold alone had to be compared
	// against the other titles to be read; a hue is apparent without
	// comparison.
	forceColor(t)
	focused := paneTitle("Graph", true, 40)
	unfocused := paneTitle("Graph", false, 40)

	if focused == unfocused {
		t.Fatal("focused and unfocused pane titles render identically")
	}
	if !strings.Contains(focused, "\x1b[1;36m") {
		t.Errorf("the focused title is not bold cyan: %q", focused)
	}
}

func TestPaneTitleDoesNotStyleEveryCharacter(t *testing.T) {
	// Underline made lipgloss emit an escape pair per character — a
	// five-letter title became a dozen sequences, which is both wasteful
	// and a hazard for the truncate-then-style rule.
	forceColor(t)
	title := paneTitle("Graph", true, 40)

	if n := strings.Count(title, "\x1b["); n > 6 {
		t.Errorf("the title emits %d escape sequences for five letters: %q", n, title)
	}
}

func TestBoxFrameSharesItsTaskColour(t *testing.T) {
	// Dim borders around a coloured label made every box look like a grey
	// frame someone had written inside.
	m := colourModel(t)
	m.snap.Tasks = []broker.TaskSnapshot{{ID: "done", State: broker.TaskCompleted}}

	rows := m.boxRows(40)
	var top, body boxRow
	for _, r := range rows {
		if len(r.spans) == 0 {
			continue
		}
		switch r.spans[0].part {
		case partTop:
			top = r
		case partBody:
			body = r
		}
	}
	if len(top.spans) == 0 || len(body.spans) == 0 {
		t.Fatal("expected a box with a top border and a body")
	}

	topOut := m.styleBoxLine(top, top.text, -1)
	bodyOut := m.styleBoxLine(body, body.text, -1)
	if !strings.Contains(topOut, "32") {
		t.Errorf("a completed task's frame is not green: %q", topOut)
	}
	if !strings.Contains(bodyOut, "32") {
		t.Errorf("a completed task's label is not green: %q", bodyOut)
	}
}

func TestTranscriptDistinguishesItsThreeKinds(t *testing.T) {
	// A tool call is what the worker did, its result is evidence, and its
	// prose is what it thinks. Rendered alike the feed is a wall of grey.
	forceColor(t)
	entries := []transcriptEntry{
		{kind: "tool", text: "Bash  send worklog \"found it\""},
		{kind: "result", text: "{\"Seq\":2}"},
		{kind: "text", text: "worklog posted, calling task-done"},
	}

	out := renderTranscript(entries, 80)
	if len(out) < 3 {
		t.Fatalf("expected a line per entry, got %v", out)
	}
	tool, result, prose := out[0], out[1], out[2]

	if !strings.Contains(tool, "\x1b[36m") {
		t.Errorf("a tool call is not cyan: %q", tool)
	}
	if !strings.Contains(result, "\x1b[2m") {
		t.Errorf("a tool result is not dimmed: %q", result)
	}
	if !strings.Contains(prose, "\x1b[32m") {
		t.Errorf("the worker's own prose is not marked: %q", prose)
	}
}

func TestRunPromptIsNotDimmed(t *testing.T) {
	// The prompt is what the run is for, and it was the dimmest thing on
	// screen.
	m := colourModel(t)
	m.snap.Prompt = "audit the auth module"

	header := m.viewHeader()
	line := strings.Split(header, "\n")[1]
	if strings.Contains(line, "\x1b[2m") {
		t.Errorf("the run prompt is dimmed: %q", line)
	}
}

func TestInspectorFieldsAreNotAllOneColour(t *testing.T) {
	// Five identical grey rows cannot be scanned; the whole point of the
	// inspector is answering a question at a glance.
	m := colourModel(t)
	m.snap.Tasks = []broker.TaskSnapshot{
		{ID: "a", State: broker.TaskDispatched, Model: "opus", Dispatches: 3},
		{ID: "b", State: broker.TaskReady, Deps: []string{"a"}},
	}
	m.taskCur = 0

	out := m.viewInspector(70, 9)
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if a := ansiFor(line); a != "" {
			seen[a] = true
		}
	}
	if len(seen) < 3 {
		t.Errorf("the inspector uses %d styles across its rows: %q", len(seen), out)
	}
}
