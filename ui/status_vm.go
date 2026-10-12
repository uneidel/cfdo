package ui

import (
	"sort"

	"github.com/ulrich/cfdo/internal/core"
)

// Field is one label/value line inside a status Card.
type Field struct {
	Label string
	Value string
	State CellState
}

// Card groups related status fields under a heading.
type Card struct {
	Title  string
	Fields []Field
}

// yesNo renders a bool as a colored yes/no cell (green when the "good" sense is true).
func yesNo(b bool) Field {
	if b {
		return Field{Value: "yes", State: StateOK}
	}
	return Field{Value: "no", State: StateMuted}
}

// yesNoPtr renders an optional bool: "—" when unknown (nil), else like yesNo. core uses
// *bool on StatusReport so an omitted flag is distinct from an explicit false.
func yesNoPtr(b *bool) Field {
	if b == nil {
		return Field{Value: "—", State: StateMuted}
	}
	return yesNo(*b)
}

// StatusCards maps a StatusReport into render-only cards. Known fields are grouped; anything
// the CLI adds that core couldn't type lands in Extra and is surfaced verbatim in a "Raw" card
// (sorted for determinism) so nothing is ever dropped. Error strings only appear when non-empty.
// Pure → unit-tested without a display.
func StatusCards(s core.StatusReport) []Card {
	cards := []Card{
		{Title: "Overview", Fields: []Field{
			{Label: "Script", Value: s.Script},
			{Label: "Class", Value: s.Class},
			{Label: "Binding", Value: s.Binding},
			withLabel("Deployed", yesNo(s.Deployed)),
			withLabel("SQLite", yesNoPtr(s.SQLite)),
			{Label: "Modified", Value: s.ModifiedOn},
			{Label: "Compat date", Value: s.CompatibilityDate},
			{Label: "Worker URL", Value: s.WorkerURL},
		}},
		{Title: "Migrations", Fields: []Field{
			{Label: "Local tag", Value: s.LocalMigrationTag},
			{Label: "Applied tag", Value: s.AppliedMigrationTag},
			{Label: "Remote tag", Value: s.RemoteMigrationTag},
			{Label: "Last uploaded", Value: s.LastUploadedAt},
		}},
		{Title: "Objects", Fields: objectsFields(s)},
	}
	if raw := rawCard(s.Extra); raw != nil {
		cards = append(cards, *raw)
	}
	return cards
}

// withLabel attaches a label to a value/state field built by a helper (e.g. yesNo).
func withLabel(label string, f Field) Field {
	f.Label = label
	return f
}

// objectsFields builds the Objects card, surfacing any object/namespace error in red.
func objectsFields(s core.StatusReport) []Field {
	fields := []Field{
		{Label: "Namespace", Value: shortID(s.NamespaceID)},
		{Label: "Object count", Value: itoaPtr(s.ObjectCount)},
		{Label: "With data", Value: itoaPtr(s.ObjectsWithData)},
		{Label: "Indexed", Value: itoaPtr(s.IndexedObjects)},
	}
	if s.ObjectsError != "" {
		fields = append(fields, Field{Label: "Objects error", Value: s.ObjectsError, State: StateError})
	}
	if s.NamespaceError != "" {
		fields = append(fields, Field{Label: "Namespace error", Value: s.NamespaceError, State: StateError})
	}
	return fields
}

// rawCard renders the Extra overflow map in sorted key order; nil when there is nothing extra.
func rawCard(extra map[string]string) *Card {
	if len(extra) == 0 {
		return nil
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fields := make([]Field, 0, len(keys))
	for _, k := range keys {
		fields = append(fields, Field{Label: k, Value: extra[k], State: StateMuted})
	}
	return &Card{Title: "Raw", Fields: fields}
}
