package main

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func md(s string, w int) string {
	return ansi.Strip(strings.Join(renderMarkdown(s, w, lipgloss.NewStyle()), "\n"))
}

// A worker's commit table reads as a table: aligned columns, no pipes, the
// separator row a rule.
func TestATableIsAligned(t *testing.T) {
	got := md("| 커밋 | 내용 |\n|---|---|\n| `29b539a` | Kiro ACP tool-name aliasing |\n| `83ac223` | idle session reaper |", 80)
	want := "커밋     내용\n" +
		"────────────────────────────────────\n" +
		"29b539a  Kiro ACP tool-name aliasing\n" +
		"83ac223  idle session reaper"
	if got != want {
		t.Errorf("table:\n%s\nwant:\n%s", got, want)
	}
}

// Markers are rendered, not printed; headings and bullets keep their shape.
func TestInlineAndBlockMarkdown(t *testing.T) {
	got := md("## 검증\n\n**항목 (3)** 전제가 `거짓`이었습니다.\n- one\n- two", 80)
	if w := "검증\n\n항목 (3) 전제가 거짓이었습니다.\n• one\n• two"; got != w {
		t.Errorf("got:\n%s\nwant:\n%s", got, w)
	}
}

// Every line fits the width, Hangul and long words included.
func TestMarkdownLinesFitTheWidth(t *testing.T) {
	text := "서술과 어긋난 지점 3건: kiro-cli 2.21.0을 직접 측정했습니다 — `session/new` 후에도 stdin EOF를 존중해 ~1초 내 rc=0으로 종료합니다. " +
		strings.Repeat("x", 70) + "\n- 길게 이어지는 bullet 항목입니다 길게 이어지는 bullet 항목입니다 길게 이어지는\n| a | b |\n|-|-|\n| " + strings.Repeat("긴", 40) + " | z |"
	for _, w := range []int{20, 33, 60} {
		for _, l := range renderMarkdown(text, w, lipgloss.NewStyle()) {
			if n := runewidth.StringWidth(ansi.Strip(l)); n > w {
				t.Errorf("width %d: line is %d cells: %q", w, n, ansi.Strip(l))
			}
		}
	}
}

func TestFlattenMarkdown(t *testing.T) {
	got := flattenMarkdown("## 결과\n\n| a | b |\n|---|---|\n| 1 | 2 |\n```\nlog\n```\n**done** `x`")
	if got != "결과 a · b 1 · 2 done x" {
		t.Errorf("got %q", got)
	}
}
