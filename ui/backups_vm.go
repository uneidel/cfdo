package ui

import "github.com/ulrich/cfdo/internal/core"

// BackupRow is one row of the Backups table (core returns entries newest-first; the view
// preserves that order). A row carries its source Dir so a Restore action can target it.
type BackupRow struct {
	Dir       string
	Script    string
	Namespace string
	Class     string
	Created   string // CreatedAt verbatim
	Objects   string
	Failures  Field // warn-colored when any object failed to back up
}

// BackupRows maps core.Backups output into display rows. Pure → unit-tested without a display.
func BackupRows(entries []core.BackupEntry) []BackupRow {
	rows := make([]BackupRow, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, BackupRow{
			Dir:       e.Dir,
			Script:    e.Script,
			Namespace: shortID(e.Namespace),
			Class:     e.Class,
			Created:   e.CreatedAt,
			Objects:   itoa(e.ObjectCount),
			Failures:  failuresField(e.Failures),
		})
	}
	return rows
}

// failuresField colors a backup's failure count: 0 → muted "0", >0 → warn.
func failuresField(n int) Field {
	if n == 0 {
		return Field{Value: "0", State: StateMuted}
	}
	return Field{Value: itoa(n), State: StateWarn}
}
