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
		// Precedence: a hard API error wins even on a shallow (non-deep) list.
		{"api error wins shallow", core.NamespaceReport{APIObjects: 5, APIError: "403 forbidden"}, false, "—", StateError},
		// Precedence: index error is reported before a count mismatch is considered,
		// so an untrustworthy index never masquerades as a reconciled disagreement.
		{"index error beats mismatch", core.NamespaceReport{APIObjects: 12, IndexObjects: intp(9), IndexError: "timeout"}, true, "12", StateWarn},
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

// TestObjectsCellTooltips asserts the human-facing hint/error detail on the hero cell,
// not just its text/color — the tooltip is where a shallow list tells the user to enable
// Deep and where index/API errors surface their cause.
func TestObjectsCellTooltips(t *testing.T) {
	if c := objectsCell(core.NamespaceReport{APIObjects: 12}, false); c.Tooltip == "" {
		t.Error("shallow cell should carry an 'enable Deep' hint tooltip")
	}
	if c := objectsCell(core.NamespaceReport{APIError: "403"}, true); c.Tooltip != "API error: 403" {
		t.Errorf("api-error tooltip = %q, want 'API error: 403'", c.Tooltip)
	}
	if c := objectsCell(core.NamespaceReport{APIObjects: 12, IndexError: "timeout"}, true); c.Tooltip != "index error: timeout" {
		t.Errorf("index-error tooltip = %q, want 'index error: timeout'", c.Tooltip)
	}
	// A clean, reconciled deep cell carries no tooltip.
	if c := objectsCell(core.NamespaceReport{APIObjects: 7, IndexObjects: intp(7)}, true); c.Tooltip != "" {
		t.Errorf("clean deep cell tooltip = %q, want empty", c.Tooltip)
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
		{at(-10 * time.Minute), "just now"},    // clock skew: a future stamp clamps to "just now", never a negative age
		{"not-a-timestamp", "not-a-timestamp"}, // unparseable → shown verbatim, never dropped
	}
	for _, c := range cases {
		if got := relTime(c.in, now); got != c.want {
			t.Errorf("relTime(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestShortID(t *testing.T) {
	// Boundary: an id up to idTrim (8) chars is shown verbatim; longer gets truncated + ellipsis.
	cases := []struct{ in, want string }{
		{"", ""},
		{"abc", "abc"},
		{"abcdef12", "abcdef12"},   // exactly idTrim → no ellipsis
		{"abcdef123", "abcdef12…"}, // one over → truncated
	}
	for _, c := range cases {
		if got := shortID(c.in); got != c.want {
			t.Errorf("shortID(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNamespaceRows_OrderAndEmpty(t *testing.T) {
	// nil/empty input → an empty (non-nil) slice, never a panic.
	if rows := NamespaceRows(nil, true, time.Now()); len(rows) != 0 {
		t.Errorf("nil items → %d rows, want 0", len(rows))
	}
	// Row order mirrors the input order exactly; the full NamespaceID is preserved
	// alongside the truncated NSShort so a copy action still has the real value.
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	items := []core.NamespaceReport{
		{Script: "a", NamespaceID: "1111222233334444"},
		{Script: "b", NamespaceID: "short"},
	}
	rows := NamespaceRows(items, false, now)
	if len(rows) != 2 || rows[0].Script != "a" || rows[1].Script != "b" {
		t.Fatalf("row order not preserved: %+v", rows)
	}
	if rows[0].NamespaceID != "1111222233334444" || rows[0].NSShort != "11112222…" {
		t.Errorf("row0 id = {full:%q short:%q}, want full preserved + truncated short", rows[0].NamespaceID, rows[0].NSShort)
	}
	if rows[1].NSShort != "short" {
		t.Errorf("row1 short id = %q, want verbatim (<= idTrim)", rows[1].NSShort)
	}
	// Shallow list → every object cell is muted with the Deep hint.
	if rows[0].Objects.State != StateMuted {
		t.Errorf("shallow row object state = %s, want muted", rows[0].Objects.State)
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
