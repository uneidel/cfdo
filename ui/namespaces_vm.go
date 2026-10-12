package ui

import (
	"fmt"
	"time"

	"github.com/ulrich/cfdo/internal/core"
)

// ObjectsCell is the hero object-count cell on a namespace row (the signature metric).
type ObjectsCell struct {
	Text    string
	State   CellState
	Tooltip string // breakdown or error detail; "" = none
}

// NSRow is one row of the Namespaces table — presentation only; core types never reach the widgets.
type NSRow struct {
	Script      string
	Class       string
	NamespaceID string // full value (for copy)
	NSShort     string // truncated for display
	SQLite      bool
	Deployed    bool
	Objects     ObjectsCell
	Modified    string
}

// NamespaceRows maps the ratified core.Namespaces result ([]NamespaceReport) into display rows.
// Pure → unit-tested without a display. `deep` reflects whether the list was fetched with --deep
// (object counts only reconcile against the index then).
func NamespaceRows(items []core.NamespaceReport, deep bool, now time.Time) []NSRow {
	rows := make([]NSRow, 0, len(items))
	for _, r := range items {
		rows = append(rows, NSRow{
			Script:      r.Script,
			Class:       r.Class,
			NamespaceID: r.NamespaceID,
			NSShort:     shortID(r.NamespaceID),
			SQLite:      r.SQLite,
			Deployed:    r.Deployed,
			Objects:     objectsCell(r, deep),
			Modified:    relTime(r.ModifiedOn, now),
		})
	}
	return rows
}

// objectsCell reconciles the api vs index object counts and surfaces any *_error inline.
//
//	hard API error      → "—", error (count is untrustworthy)
//	shallow list        → api count, muted + "enable Deep" hint
//	deep + index error  → api count, warn + index-error tooltip
//	deep + mismatch     → "N api / M idx", warn
//	deep + agreement    → api count, ok
func objectsCell(r core.NamespaceReport, deep bool) ObjectsCell {
	if r.APIError != "" {
		return ObjectsCell{Text: "—", State: StateError, Tooltip: "API error: " + r.APIError}
	}
	if !deep {
		return ObjectsCell{
			Text:    fmt.Sprintf("%d", r.APIObjects),
			State:   StateMuted,
			Tooltip: "enable Deep for verified object counts",
		}
	}
	if r.IndexError != "" {
		return ObjectsCell{
			Text:    fmt.Sprintf("%d", r.APIObjects),
			State:   StateWarn,
			Tooltip: "index error: " + r.IndexError,
		}
	}
	if r.IndexObjects != nil && *r.IndexObjects != r.APIObjects {
		return ObjectsCell{
			Text:    fmt.Sprintf("%d api / %d idx", r.APIObjects, *r.IndexObjects),
			State:   StateWarn,
			Tooltip: "api/index object-count mismatch",
		}
	}
	return ObjectsCell{Text: fmt.Sprintf("%d", r.APIObjects), State: StateOK}
}
