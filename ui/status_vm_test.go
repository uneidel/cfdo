package ui

import (
	"testing"

	"github.com/ulrich/cfdo/internal/core"
)

func boolp(b bool) *bool { return &b }

// cardByTitle finds a rendered card by heading.
func cardByTitle(cards []Card, title string) (Card, bool) {
	for _, c := range cards {
		if c.Title == title {
			return c, true
		}
	}
	return Card{}, false
}

// fieldByLabel finds a field within a card.
func fieldByLabel(c Card, label string) (Field, bool) {
	for _, f := range c.Fields {
		if f.Label == label {
			return f, true
		}
	}
	return Field{}, false
}

func TestStatusCards_DeployedColoring(t *testing.T) {
	cards := StatusCards(core.StatusReport{Script: "chat", Deployed: true, SQLite: boolp(false)})
	ov, ok := cardByTitle(cards, "Overview")
	if !ok {
		t.Fatal("no Overview card")
	}
	dep, _ := fieldByLabel(ov, "Deployed")
	if dep.Value != "yes" || dep.State != StateOK {
		t.Errorf("Deployed = {%q,%s}, want {yes,ok}", dep.Value, dep.State)
	}
	sq, _ := fieldByLabel(ov, "SQLite")
	if sq.Value != "no" || sq.State != StateMuted {
		t.Errorf("SQLite = {%q,%s}, want {no,muted}", sq.Value, sq.State)
	}
}

func TestStatusCards_ErrorsSurfaceRed(t *testing.T) {
	cards := StatusCards(core.StatusReport{ObjectsError: "429 rate limited"})
	obj, ok := cardByTitle(cards, "Objects")
	if !ok {
		t.Fatal("no Objects card")
	}
	oe, found := fieldByLabel(obj, "Objects error")
	if !found {
		t.Fatal("expected an Objects error field")
	}
	if oe.State != StateError || oe.Value != "429 rate limited" {
		t.Errorf("Objects error = {%q,%s}, want {429 rate limited,error}", oe.Value, oe.State)
	}
	// A clean report must NOT carry the error field.
	clean := StatusCards(core.StatusReport{})
	co, _ := cardByTitle(clean, "Objects")
	if _, found := fieldByLabel(co, "Objects error"); found {
		t.Error("clean report should not have an Objects error field")
	}
}

func TestStatusCards_RawOverflowSortedAndDroppedWhenEmpty(t *testing.T) {
	// No Extra → no Raw card.
	if _, ok := cardByTitle(StatusCards(core.StatusReport{}), "Raw"); ok {
		t.Error("empty Extra should produce no Raw card")
	}
	// Extra present → Raw card, keys sorted, nothing dropped.
	cards := StatusCards(core.StatusReport{Extra: map[string]string{
		"zeta":  "26",
		"alpha": "1",
		"mu":    "12",
	}})
	raw, ok := cardByTitle(cards, "Raw")
	if !ok {
		t.Fatal("expected a Raw card")
	}
	if len(raw.Fields) != 3 {
		t.Fatalf("Raw dropped keys: got %d, want 3", len(raw.Fields))
	}
	wantOrder := []string{"alpha", "mu", "zeta"}
	for i, w := range wantOrder {
		if raw.Fields[i].Label != w {
			t.Errorf("Raw field %d = %q, want %q (sorted)", i, raw.Fields[i].Label, w)
		}
	}
}
