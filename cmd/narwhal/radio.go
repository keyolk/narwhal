// radio.go draws pane 4's radio as cards rather than a log.
//
// The radio was one line per message, cut at the pane's edge. On a real run
// that line had little to say: of 46 messages, 15 were file claims listing
// paths, 13 opened with a tag repeating their sender ("[synthesis]" from
// synthesis), and the median body ran to 1,151 characters, so every row
// was the first sixty of them. Urgent posts were painted bold red end to
// end, which on a busy run turned the pane red.
//
// Each message is now a card: time, sender — once for a run of messages
// from the same one — and up to two lines of its text as prose, markdown
// flattened, with the tag that repeats the sender dropped. Coordination
// traffic folds: a run of claims and releases is one dim line. An urgent
// post is marked by a red bar and a bold body, not a red one; the
// operator's own by a yellow bar.
package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/keyolk/tuikit/render"
	"github.com/mattn/go-runewidth"

	"github.com/keyolk/narwhal/internal/broker"
)

// radioBlock is a run of messages drawn as one card: a single message, or
// consecutive coordination traffic folded together.
type radioBlock struct{ first, last int }

// radioSenderW is the sender column; longer names are cut.
const radioSenderW = 12

// radioBlocks groups the messages into cards.
func (m tuiModel) radioBlocks() []radioBlock {
	var out []radioBlock
	for i, msg := range m.snap.Messages {
		_, proto := radioSummary(msgContent(msg))
		if proto && len(out) > 0 {
			prev := &out[len(out)-1]
			if _, p := radioSummary(msgContent(m.snap.Messages[prev.last])); p && prev.last == i-1 {
				prev.last = i
				continue
			}
		}
		out = append(out, radioBlock{i, i})
	}
	return out
}

func msgContent(msg *broker.Message) string {
	if msg == nil {
		return ""
	}
	return msg.Content
}

// blockOf is the index of the card holding message i.
func blockOf(blocks []radioBlock, i int) int {
	for b, bl := range blocks {
		if i >= bl.first && i <= bl.last {
			return b
		}
	}
	return max(len(blocks)-1, 0)
}

// stepRadio moves the radio cursor by cards, not messages: a folded run of
// claims is one stop.
func (m *tuiModel) stepRadio(delta int) {
	blocks := m.radioBlocks()
	if len(blocks) == 0 {
		return
	}
	b := min(max(blockOf(blocks, m.radioCur)+delta, 0), len(blocks)-1)
	m.radioCur = blocks[b].first
	if b == len(blocks)-1 {
		m.radioCur = blocks[b].last
	}
}

// viewRadio draws the cards that fit, keeping the selected one on screen:
// following, the newest at the bottom; otherwise the selection with what
// came after it and before it.
func (m tuiModel) viewRadio(width, height int) string {
	rows := []string{m.channelTitle(width)}
	blocks := m.radioBlocks()
	if len(blocks) == 0 {
		rows = append(rows, styDim.Render("(no messages yet)"))
		return padRows(rows, width, height)
	}
	sel := blockOf(blocks, m.radioCur)
	selected := -1
	if m.focus == focusRadio {
		selected = sel
	}
	cache := map[int][]string{}
	card := func(b int) []string {
		if c, ok := cache[b]; ok {
			return c
		}
		prev := ""
		if b > 0 {
			prev = m.snap.Messages[blocks[b-1].last].Sender
		}
		c := m.radioCard(blocks[b], prev, width, b == selected)
		cache[b] = c
		return c
	}

	avail := max(1, height-1)
	lo, hi := sel, sel
	used := len(card(sel))
	// Below the selection first, up to half the room, unless following.
	if !m.followTail {
		for hi+1 < len(blocks) && used+len(card(hi+1)) <= avail-(avail-used)/2 {
			hi++
			used += len(card(hi))
		}
	}
	for lo > 0 && used+len(card(lo-1)) <= avail {
		lo--
		used += len(card(lo))
	}
	for hi+1 < len(blocks) && used+len(card(hi+1)) <= avail {
		hi++
		used += len(card(hi))
	}
	for b := lo; b <= hi; b++ {
		rows = append(rows, card(b)...)
	}
	return padRows(rows, width, height)
}

// radioRow is a message's card as one string, for callers that want a
// single message drawn on its own.
func (m tuiModel) radioRow(msg *broker.Message, width int, selected bool) string {
	saved := m.snap.Messages
	m.snap.Messages = []*broker.Message{msg}
	defer func() { m.snap.Messages = saved }()
	return strings.Join(m.radioCard(radioBlock{0, 0}, "", width, selected), "\n")
}

var leadTag = regexp.MustCompile(`^\[([^\]\s]{1,40})\]\s*`)

// radioBody is the text a card shows: a protocol message as a sentence,
// anything else as prose, without a leading tag that names its sender.
func radioBody(msg *broker.Message) (string, bool) {
	text, proto := radioSummary(msg.Content)
	if proto {
		return text, true
	}
	text = msg.Content
	if mt := leadTag.FindStringSubmatch(text); mt != nil {
		tag := strings.ToLower(mt[1])
		if tag == shortName(msg.Sender) || (msg.Sender == "main" && tag == "operator") {
			text = text[len(mt[0]):]
		}
	}
	return flattenMarkdown(text), false
}

// radioCard draws a block. prevSender is the sender of the card above, so
// a run from one sender names it once.
func (m tuiModel) radioCard(bl radioBlock, prevSender string, width int, selected bool) []string {
	msgs := m.snap.Messages[bl.first : bl.last+1]
	msg := msgs[0]
	stamp := clock(msg.CreatedAt)
	sender := shortName(msg.Sender)
	shownSender := sender
	if msg.Sender == prevSender && len(msgs) == 1 {
		shownSender = ""
	}
	senderCol := render.PadRight(render.Truncate(shownSender, radioSenderW), radioSenderW)
	head := " " + stamp + " " + senderCol + " "
	bodyW := max(8, width-runewidth.StringWidth(head)-1)

	bar, barStyle := " ", lipgloss.NewStyle()
	bodyStyle := lipgloss.NewStyle()
	senderStyle := styBlue
	var text string
	var proto bool
	switch {
	case len(msgs) > 1:
		// A run of coordination traffic: how much, and from whom.
		var who []string
		seen := map[string]bool{}
		for _, x := range msgs {
			if s := shortName(x.Sender); !seen[s] {
				seen[s] = true
				who = append(who, s)
			}
		}
		text, proto = fmt.Sprintf("⋮ %d claims and releases · %s", len(msgs), strings.Join(who, ", ")), true
		senderCol = strings.Repeat(" ", radioSenderW)
	default:
		text, proto = radioBody(msg)
		if proto {
			text = "⋮ " + strings.TrimPrefix(text, sender+" ")
		}
	}
	switch {
	case proto:
		bodyStyle = styDim
	case msg.Sender == "main":
		bar, barStyle, bodyStyle, senderStyle = "▌", styYellowBold, styYellow, styYellowBold
	case msg.Priority == broker.PriorityUrgent:
		// Marked, not painted: a bar and weight, so an urgent post stands
		// out without a run of them turning the pane red.
		bar, barStyle, bodyStyle = "▌", styRedBold, lipgloss.NewStyle().Bold(true)
	}

	mention := ""
	if !proto && len(msg.Mentions) > 0 && !strings.HasPrefix(msg.Content, broker.RelayPrefix) {
		var to []string
		for _, id := range msg.Mentions {
			to = append(to, shortName(id))
		}
		mention = "→" + strings.Join(to, ",") + " "
	}

	// Two lines of the body at most; a cut says so.
	lines := 2
	if proto {
		lines = 1
	}
	wrapped := wrapSpans([]span{{mention + text, lipgloss.NewStyle()}}, bodyW, "")
	if len(wrapped) > lines {
		wrapped = wrapped[:lines]
		last := wrapped[lines-1]
		wrapped[lines-1] = render.TruncateMarker(last, bodyW-1, "") + render.Ellipsis()
	}

	if selected {
		var out []string
		for i, l := range wrapped {
			prefix := strings.Repeat(" ", runewidth.StringWidth(head)+1)
			if i == 0 {
				prefix = bar + head
				if shownSender == "" && len(msgs) == 1 {
					prefix = bar + " " + stamp + " " + senderCol + " "
				}
			}
			out = append(out, stySel.Render(render.PadRight(render.TruncateMarker(prefix+l, width, ""), width)))
		}
		return out
	}

	var out []string
	for i, l := range wrapped {
		body := l
		if i == 0 && mention != "" && strings.HasPrefix(body, mention) {
			body = styMagenta.Render(mention) + bodyStyle.Render(strings.TrimPrefix(body, mention))
		} else {
			body = bodyStyle.Render(body)
		}
		if i == 0 {
			out = append(out, barStyle.Render(bar)+" "+styDim.Render(stamp)+" "+
				senderStyle.Render(senderCol)+" "+body)
			continue
		}
		out = append(out, barStyle.Render(bar)+strings.Repeat(" ", runewidth.StringWidth(head))+body)
	}
	for i := range out {
		out[i] = ansi.Truncate(out[i], width, "")
	}
	return out
}
