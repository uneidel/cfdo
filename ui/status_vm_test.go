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

func TestStatusCards_ObjectCountsOptional(t *testing.T) {
	// A report that carries real counts must render the numbers; a report that omits
	// them (nil *int) must render "—" rather than a misleading 0. This is the itoaPtr
	// contract — "omitted" is distinct from an explicit zero.
	withCounts := StatusCards(core.StatusReport{
		NamespaceID:     "abcdef1234567890",
		ObjectCount:     intp(40),
		ObjectsWithData: intp(31),
		IndexedObjects:  intp(0), // a real zero, NOT omitted → "0", never "—"
	})
	obj, ok := cardByTitle(withCounts, "Objects")
	if !ok {
		t.Fatal("no Objects card")
	}
	for _, want := range []struct{ label, value string }{
		{"Namespace", "abcdef12…"}, // full id truncated for the cell
		{"Object count", "40"},
		{"With data", "31"},
		{"Indexed", "0"}, // explicit zero shows "0"
	} {
		f, found := fieldByLabel(obj, want.label)
		if !found {
			t.Fatalf("Objects card missing %q field", want.label)
		}
		if f.Value != want.value {
			t.Errorf("%s = %q, want %q", want.label, f.Value, want.value)
		}
	}

	// Omitted counts → em dash, so an absent metric is never shown as a real 0.
	omitted := StatusCards(core.StatusReport{})
	oc, _ := cardByTitle(omitted, "Objects")
	for _, label := range []string{"Object count", "With data", "Indexed"} {
		f, _ := fieldByLabel(oc, label)
		if f.Value != "—" {
			t.Errorf("omitted %s = %q, want em dash", label, f.Value)
		}
	}
}

func TestStatusCards_NamespaceErrorSurfacesRed(t *testing.T) {
	// Both object- and namespace-level errors must surface, each red, in that order,
	// appended after the fixed count fields — neither hides the other.
	cards := StatusCards(core.StatusReport{
		ObjectsError:   "429 rate limited",
		NamespaceError: "404 namespace not found",
	})
	obj, ok := cardByTitle(cards, "Objects")
	if !ok {
		t.Fatal("no Objects card")
	}
	oe, foundO := fieldByLabel(obj, "Objects error")
	ne, foundN := fieldByLabel(obj, "Namespace error")
	if !foundO || !foundN {
		t.Fatalf("expected both error fields, got objects=%v namespace=%v", foundO, foundN)
	}
	if ne.State != StateError || ne.Value != "404 namespace not found" {
		t.Errorf("Namespace error = {%q,%s}, want {404 namespace not found,error}", ne.Value, ne.State)
	}
	if oe.State != StateError {
		t.Errorf("Objects error state = %s, want error", oe.State)
	}
	// Order: the fixed count fields come first, then objects error, then namespace error.
	var io, in int = -1, -1
	for i, f := range obj.Fields {
		switch f.Label {
		case "Objects error":
			io = i
		case "Namespace error":
			in = i
		}
	}
	if !(io >= 0 && in > io) {
		t.Errorf("error fields out of order: objects at %d, namespace at %d", io, in)
	}
}

func TestStatusCards_OverviewAndMigrationsVerbatim(t *testing.T) {
	// Overview/Migrations are plain pass-through cards; a field the CLI reports must
	// reach the view unaltered (no hiding, no reformatting) so nothing is dropped.
	s := core.StatusReport{
		Script: "chat", Class: "Room", Binding: "CHAT",
		CompatibilityDate: "2026-01-01", ModifiedOn: "2026-10-10T08:00:00Z",
		WorkerURL:           "https://chat.example.workers.dev",
		LocalMigrationTag:   "v3",
		AppliedMigrationTag: "v2",
		RemoteMigrationTag:  "v2",
		LastUploadedAt:      "2026-10-09T12:00:00Z",
	}
	cards := StatusCards(s)
	ov, _ := cardByTitle(cards, "Overview")
	for _, want := range []struct{ label, value string }{
		{"Script", "chat"}, {"Class", "Room"}, {"Binding", "CHAT"},
		{"Compat date", "2026-01-01"}, {"Modified", "2026-10-10T08:00:00Z"},
		{"Worker URL", "https://chat.example.workers.dev"},
	} {
		f, found := fieldByLabel(ov, want.label)
		if !found || f.Value != want.value {
			t.Errorf("Overview %s = {%q,found=%v}, want %q", want.label, f.Value, found, want.value)
		}
	}
	mig, ok := cardByTitle(cards, "Migrations")
	if !ok {
		t.Fatal("no Migrations card")
	}
	for _, want := range []struct{ label, value string }{
		{"Local tag", "v3"}, {"Applied tag", "v2"}, {"Remote tag", "v2"},
		{"Last uploaded", "2026-10-09T12:00:00Z"},
	} {
		f, found := fieldByLabel(mig, want.label)
		if !found || f.Value != want.value {
			t.Errorf("Migrations %s = {%q,found=%v}, want %q", want.label, f.Value, found, want.value)
		}
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
