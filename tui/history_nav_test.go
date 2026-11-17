package tui

import (
	"testing"
)

// histModel is a model whose composer history holds entries, oldest first.
func histModel(entries ...string) *model {
	m := newTestModel()
	m.width, m.height = 80, 40
	for _, e := range entries {
		m.composer.addHistory(e, nil)
	}
	return m
}

func press(m *model, keys ...string) {
	for _, k := range keys {
		m.Update(keyNamed(k))
	}
}

// Arrows inside a recalled message edit it; they don't swap it for
// another history entry (Codex should_handle_navigation).
func TestHistoryArrowsEditRecalledMessage(t *testing.T) {
	multi := "first line of the message\nsecond line\nthird line"
	m := histModel("hello", multi)

	press(m, "up")
	if got := m.composer.String(); got != multi {
		t.Fatalf("Up on an empty editor recalls the newest entry, got %q", got)
	}
	// Into the text, then up: the cursor moves to the line above.
	press(m, "left", "left", "up")
	if got := m.composer.String(); got != multi {
		t.Fatalf("Up inside a recalled message replaced it with %q", got)
	}
	if line := lineOf(m.composer); line != 1 {
		t.Errorf("cursor on line %d, want 1 (the line above)", line)
	}
	press(m, "up")
	if got, line := m.composer.String(), lineOf(m.composer); got != multi || line != 0 {
		t.Errorf("second Up: text %q, line %d", got, line)
	}
	press(m, "down", "down")
	if got := m.composer.String(); got != multi {
		t.Fatalf("Down inside a recalled message replaced it with %q", got)
	}

	// Edited text is never swapped, even with the cursor at its end.
	m = histModel("hello", "world")
	press(m, "up")
	m.Update(keyNamed("!"))
	press(m, "up", "down")
	if got := m.composer.String(); got != "world!" {
		t.Errorf("an edited entry was replaced: %q", got)
	}
}

// Unedited, at the start or end, the arrows still browse like a shell.
func TestHistoryArrowsStillBrowse(t *testing.T) {
	m := histModel("one", "two", "three")
	press(m, "up", "up")
	if got := m.composer.String(); got != "two" {
		t.Fatalf("Up Up: %q", got)
	}
	// From inside a single-line entry: Up goes to its start, then browses.
	press(m, "left", "up")
	if got := m.composer.String(); got != "two" || m.composer.cursor != 0 {
		t.Fatalf("Up from inside: %q at %d", got, m.composer.cursor)
	}
	press(m, "up")
	if got := m.composer.String(); got != "one" {
		t.Errorf("Up at the start: %q", got)
	}
	press(m, "down", "down", "down")
	if got := m.composer.String(); got != "" {
		t.Errorf("Down past the newest: %q", got)
	}
	// A typed draft isn't left by Up at its start (Codex).
	m = histModel("one")
	m.composer.Set("draft")
	m.composer.cursor = 0
	press(m, "up")
	if got := m.composer.String(); got != "draft" {
		t.Errorf("Up on a draft recalled history: %q", got)
	}
}

// Browsing history opens no "/" or "@" popup for a recalled entry.
func TestHistoryBrowsingOpensNoPopup(t *testing.T) {
	m := histModel("/model", "look at @main.go")
	press(m, "up")
	if m.composer.files != nil || m.composer.slash != nil {
		t.Errorf("popup opened for a recalled @ entry")
	}
	press(m, "up")
	if m.composer.String() != "/model" || m.composer.slash != nil {
		t.Errorf("recalled %q, slash popup open: %v", m.composer.String(), m.composer.slash != nil)
	}
	press(m, "down")
	if m.composer.String() != "look at @main.go" {
		t.Errorf("Down: %q", m.composer.String())
	}
	// Once edited, it is ordinary input again.
	press(m, "left", "left", "left", "left", "left")
	m.Update(keyNamed("x"))
	m.Update(keyNamed("backspace"))
	if m.composer.files == nil {
		t.Errorf("no @ popup while editing the token")
	}
}

func lineOf(c *composer) int {
	n := 0
	for _, r := range c.text[:c.cursor] {
		if r == '\n' {
			n++
		}
	}
	return n
}
