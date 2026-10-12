package ui

import (
	"testing"
	"time"

	"github.com/ulrich/cfdo/internal/core"
)

func intp(i int) *int { return &i }

func TestObjectsCell(t *testing.T) {
	tests := []struct {
		name      string
		rep       core.NamespaceReport
		deep      bool
		wantText  string
		wantState CellState
	}{
		{"api error wins", core.NamespaceReport{APIObjects: 5, APIError: "403 forbidden"}, true, "—", StateError},
		{"shallow needs deep", core.NamespaceReport{APIObjects: 12}, false, "12", StateMuted},
		{"deep clean", core.NamespaceReport{APIObjects: 12, IndexObjects: intp(12)}, true, "12", StateOK},
		{"deep mismatch", core.NamespaceReport{APIObjects: 12, IndexObjects: intp(10)}, true, "12 api / 10 idx", StateWarn},
		{"deep index error", core.NamespaceReport{APIObjects: 12, IndexError: "timeout"}, true, "12", StateWarn},
		{"deep no index data", core.NamespaceReport{APIObjects: 0}, true, "0", StateOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := objectsCell(tt.rep, tt.deep)
			if got.Text != tt.wantText || got.State != tt.wantState {
				t.Fatalf("objectsCell = {%q,%s}, want {%q,%s}", got.Text, got.State, tt.wantText, tt.wantState)
			}
		})
	}
}

func TestRelTime(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{at(30 * time.Second), "just now"},
		{at(5 * time.Minute), "5m"},
		{at(3 * time.Hour), "3h"},
		{at(48 * time.Hour), "2d"},
		{"not-a-timestamp", "not-a-timestamp"}, // unparseable → shown verbatim, never dropped
	}
	for _, c := range cases {
		if got := relTime(c.in, now); got != c.want {
			t.Errorf("relTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNamespaceRows(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	mod := now.Add(-2 * time.Hour).Format(time.RFC3339)
	items := []core.NamespaceReport{
		{Script: "chat", Class: "Room", NamespaceID: "abcdef1234567890", SQLite: true, Deployed: true,
			ModifiedOn: mod, APIObjects: 7, IndexObjects: intp(7)},
	}
	rows := NamespaceRows(items, true, now)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.NSShort != "abcdef12…" {
		t.Errorf("NSShort = %q, want %q", r.NSShort, "abcdef12…")
	}
	if r.Modified != "2h" {
		t.Errorf("Modified = %q, want %q", r.Modified, "2h")
	}
	if r.Objects.State != StateOK || r.Objects.Text != "7" {
		t.Errorf("Objects = {%q,%s}, want {7,ok}", r.Objects.Text, r.Objects.State)
	}
}
