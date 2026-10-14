// questions.go is the monitor's view of what a run's workers asked the
// operator, answered together. See broker/questions.go.
//
// Q opens it from a run. Open questions come first, each with the task that
// asked, what it will assume meanwhile, and whether it is waiting. enter
// writes an answer, y takes the assumption as the answer; answers are held
// until S sends them all at once — the point is one sitting, not a dialogue
// per question. A blocking question unblocks its worker; an answer that
// differs from the assumption reaches the task (told while it runs,
// reopened once it has finished), and what builds on it follows.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/keys"

	"github.com/keyolk/narwhal/internal/broker"
)

// pendingAnswer is an answer written in the questions view, not yet sent.
type pendingAnswer struct {
	text      string
	asAssumed bool
}

// answersSentMsg reports a batch of answers sent.
type answersSentMsg struct {
	answered int
	reopened []string
	errs     []string
	err      error
}

// viewQuestions lists the run's questions, open ones first.
func (m tuiModel) questionsList() []broker.Question {
	qs := append([]broker.Question(nil), m.snap.Questions...)
	rank := func(q broker.Question) int {
		switch {
		case q.ForOperator():
			return 0
		case q.Candidate:
			return 1
		}
		return 2
	}
	sort.SliceStable(qs, func(i, j int) bool {
		if rank(qs[i]) != rank(qs[j]) {
			return rank(qs[i]) < rank(qs[j])
		}
		if qs[i].Blocking != qs[j].Blocking {
			return qs[i].Blocking
		}
		return qs[i].ID < qs[j].ID
	})
	return qs
}

// openQuestionCount is how many of the run's questions wait, and how many
// of those block a worker.
func openQuestionCount(s broker.Snapshot) (open, blocking int) {
	for _, q := range s.Questions {
		if q.ForOperator() {
			open++
			if q.Blocking {
				blocking++
			}
		}
	}
	return open, blocking
}

// reviewingCount is how many questions wait for the run's synthesis or
// checkpoint before they may reach the operator.
func reviewingCount(s broker.Snapshot) int {
	n := 0
	for _, q := range s.Questions {
		if q.Open() && q.Candidate {
			n++
		}
	}
	return n
}

func (m *tuiModel) openQuestions() {
	m.questionsView, m.questionSel, m.answering = true, 0, false
	if m.pendingAnswers == nil {
		m.pendingAnswers = map[int]pendingAnswer{}
	}
}

// handleAnswerKey drives the answer being written: every key belongs to
// its line, and arrives as typed.
func (m tuiModel) handleAnswerKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	qs := m.questionsList()
	b, _ := m.keymap().ResolveRaw([]keys.Ctx{ctxAnswer}, msg, m.keyOpts())
	switch b.Action {
	case "answer.cancel":
		m.answering, m.answerBuf = false, nil
		return m, nil
	case "quit":
		m.quit = true
		return m, tea.Quit
	case "answer.keep":
		if text := strings.TrimSpace(string(m.answerBuf)); text != "" && m.questionSel < len(qs) {
			m.pendingAnswers[qs[m.questionSel].ID] = pendingAnswer{text: text}
			m.questionSel = nextOpen(qs, m.questionSel, m.pendingAnswers)
		}
		m.answering, m.answerBuf = false, nil
		return m, nil
	}
	switch msg.Type {
	case tea.KeyBackspace:
		if n := len(m.answerBuf); n > 0 {
			m.answerBuf = m.answerBuf[:n-1]
		}
	case tea.KeySpace:
		m.answerBuf = append(m.answerBuf, ' ')
	case tea.KeyRunes:
		m.answerBuf = append(m.answerBuf, msg.Runes...)
	}
	return m, nil
}

// doQuestions runs the questions view's keys.
func (m tuiModel) doQuestions(action string, n int) (tea.Model, tea.Cmd) {
	qs := m.questionsList()
	var sel *broker.Question
	if m.questionSel < len(qs) {
		sel = &qs[m.questionSel]
	}
	switch action {
	case "questions.back":
		m.questionsView = false
	case "questions.down":
		if m.questionSel < len(qs)-1 {
			m.questionSel++
		}
	case "questions.up":
		if m.questionSel > 0 {
			m.questionSel--
		}
	case "questions.pick":
		// An option, for a question that offers them.
		if sel != nil && sel.ForOperator() && n >= 1 && n <= len(sel.Options) {
			m.pendingAnswers[sel.ID] = pendingAnswer{text: sel.Options[n-1]}
			m.questionSel = nextOpen(qs, m.questionSel, m.pendingAnswers)
		}
	case "questions.answer":
		if sel != nil && sel.ForOperator() {
			m.answering = true
			m.answerBuf = []rune(m.pendingAnswers[sel.ID].text)
		}
	case "questions.assume":
		if sel != nil && sel.ForOperator() && sel.Assumed != "" {
			m.pendingAnswers[sel.ID] = pendingAnswer{asAssumed: true}
			m.questionSel = nextOpen(qs, m.questionSel, m.pendingAnswers)
		}
	case "questions.clear":
		if sel != nil {
			delete(m.pendingAnswers, sel.ID)
		}
	case "questions.send":
		if len(m.pendingAnswers) == 0 {
			m.setNotice("", fmt.Errorf("no answers written yet — enter writes one, y takes the assumption"))
			return m, nil
		}
		return m, m.sendAnswers()
	}
	return m, nil
}

// nextOpen is the next open question without a pending answer, after i, or
// i when there is none.
func nextOpen(qs []broker.Question, i int, pending map[int]pendingAnswer) int {
	for j := i + 1; j < len(qs); j++ {
		if _, ok := pending[qs[j].ID]; qs[j].ForOperator() && !ok {
			return j
		}
	}
	return i
}

// sendAnswers posts the pending answers in one call.
func (m tuiModel) sendAnswers() tea.Cmd {
	base := m.live.BrokerURL
	runID := m.runID()
	client := m.client
	type ans struct {
		ID        int    `json:"id"`
		Answer    string `json:"answer,omitempty"`
		AsAssumed bool   `json:"as_assumed,omitempty"`
	}
	var list []ans
	for id, p := range m.pendingAnswers {
		list = append(list, ans{ID: id, Answer: p.text, AsAssumed: p.asAssumed})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return func() tea.Msg {
		if base == "" {
			u, err := daemonURL()
			if err != nil {
				return answersSentMsg{err: fmt.Errorf("no daemon is running: %w", err)}
			}
			base = u
		}
		body, _ := json.Marshal(map[string]any{"run_id": runID, "answers": list})
		resp, err := client.Post(base+"/api/v1/control/answer", "application/json", bytes.NewReader(body))
		if err != nil {
			return answersSentMsg{err: err}
		}
		defer resp.Body.Close()
		var out struct {
			Answered int      `json:"answered"`
			Reopened []string `json:"reopened"`
			Errors   []string `json:"errors"`
			Error    string   `json:"error"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return answersSentMsg{err: fmt.Errorf("daemon answer: %w", err)}
		}
		if resp.StatusCode != http.StatusOK {
			return answersSentMsg{err: fmt.Errorf("daemon: %s", out.Error)}
		}
		return answersSentMsg{answered: out.Answered, reopened: out.Reopened, errs: out.Errors}
	}
}

// answersSent clears what was sent and says what came of it.
func (m tuiModel) answersSent(msg answersSentMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setNotice("", msg.err)
		return m, nil
	}
	m.pendingAnswers = map[int]pendingAnswer{}
	note := fmt.Sprintf("sent %d answers", msg.answered)
	if len(msg.reopened) > 0 {
		note += " — reopened " + strings.Join(msg.reopened, ", ") + " to follow them"
	}
	if len(msg.errs) > 0 {
		m.setNotice("", fmt.Errorf("%s; %s", note, strings.Join(msg.errs, "; ")))
	} else {
		m.setNotice(note, nil)
	}
	cmd := m.poll()
	return m, cmd
}

// viewQuestions draws the questions view.
func (m tuiModel) viewQuestions() string {
	w := max(m.width, 1)
	qs := m.questionsList()
	open, blocking := openQuestionCount(m.snap)
	head := styDim.Render("‹ ") + styCyanBold.Render("Questions") +
		styDim.Render(fmt.Sprintf("   %d open", open))
	if blocking > 0 {
		head += "  " + styRedBold.Render(fmt.Sprintf("%d waiting", blocking))
	}
	if n := reviewingCount(m.snap); n > 0 {
		head += "  " + styDim.Render(fmt.Sprintf("%d in review", n))
	}
	if n := len(m.pendingAnswers); n > 0 {
		head += "  " + styYellow.Render(fmt.Sprintf("%d answers to send", n))
	}
	title := "  " + truncate(oneLine(m.snap.Prompt), w-3)

	var body []string
	if len(qs) == 0 {
		body = append(body, "", styDim.Render("  No questions. Workers ask here when the request or the code leaves a choice open."))
	}
	inner := max(10, w-8)
	for i, q := range qs {
		if i > 0 {
			body = append(body, "")
		}
		mark, st := "?", styYellow
		switch {
		case !q.Open():
			mark, st = "✓", styGreen
		case q.Candidate:
			mark, st = "…", styDim
		case q.Blocking:
			mark, st = "!", styRedBold
		}
		p, pending := m.pendingAnswers[q.ID]
		if pending {
			mark, st = "→", styCyan
		}
		lead := "  "
		if i == m.questionSel {
			lead = styCyanBold.Render("▌ ")
		}
		who := q.TaskID
		if who == broker.PlanAsker {
			who = "planner"
		}
		line := fmt.Sprintf("%s %s  %s", st.Render(mark), styCyan.Render(fmt.Sprintf("#%d %s", q.ID, who)), q.Text)
		if q.Blocking && q.Open() {
			line += styRedBold.Render("  (waiting for you)")
		}
		for j, l := range wrapText(line, inner) {
			if j == 0 {
				body = append(body, lead+l)
			} else {
				body = append(body, "     "+l)
			}
		}
		if q.Context != "" {
			for _, l := range wrapText(q.Context, inner-4) {
				body = append(body, "     "+styDim.Render(l))
			}
		}
		// What the question rests on, found for the operator: the
		// definitions of labels it uses, or the asking task's assignment.
		for _, g := range q.Grounds {
			for j, l := range wrapText(g, inner-6) {
				lead := "     " + styDim.Render("↳ ")
				if j > 0 {
					lead = "       "
				}
				body = append(body, lead+styDim.Render(l))
			}
		}
		for k, o := range q.Options {
			opt := fmt.Sprintf("%d) %s", k+1, o)
			if k+1 == q.Recommended {
				opt += "  ← recommended"
			}
			if q.ForOperator() {
				body = append(body, "     "+styCyan.Render(truncate(opt, inner-4)))
			} else {
				body = append(body, "     "+styDim.Render(truncate(opt, inner-4)))
			}
		}
		if q.Assumed != "" {
			body = append(body, "     "+styDim.Render("assumes: ")+truncate(q.Assumed, inner-9))
		}
		if q.Candidate {
			body = append(body, "     "+styDim.Render("waiting for the run's review — it may be settled from the findings first"))
		}
		if q.RaisedBy != "" && q.ForOperator() {
			body = append(body, "     "+styDim.Render("raised by "+q.RaisedBy+" after reviewing the findings"))
		}
		switch {
		case !q.Open() && q.AcceptedAssumption:
			body = append(body, "     "+styGreen.Render("answered: as assumed"))
		case !q.Open() && q.ResolvedBy != "":
			body = append(body, "     "+styGreen.Render("settled by "+q.ResolvedBy+": ")+truncate(q.Answer, inner-14))
		case !q.Open():
			body = append(body, "     "+styGreen.Render("answered: ")+truncate(q.Answer, inner-10))
		case pending && p.asAssumed:
			body = append(body, "     "+styCyan.Render("to send: as assumed"))
		case pending:
			body = append(body, "     "+styCyan.Render("to send: ")+truncate(p.text, inner-9))
		}
	}

	foot := m.footerBlock(w, m.keyFooter(m.screenStack(), w))
	if m.answering {
		foot = ansi.Truncate(styCyanBold.Render("» answer: ")+string(m.answerBuf)+styCyan.Render("▏"), w, "…") +
			"\n" + m.keyFooter([]keys.Ctx{ctxAnswer}, w) + styDim.Render(" — S sends every kept answer at once")
	}
	avail := max(1, m.height-2-strings.Count(foot, "\n")-1)
	start := 0
	if len(body) > avail {
		// Keep the selected question in view.
		at := 0
		for i := 0; i < len(body) && i < len(body); i++ {
			if strings.HasPrefix(body[i], styCyanBold.Render("▌ ")) {
				at = i
				break
			}
		}
		start = min(max(0, at-avail/3), len(body)-avail)
	}
	end := min(len(body), start+avail)
	return pinFooter(head+"\n"+title+"\n"+strings.Join(body[start:end], "\n"), foot, m.height)
}
