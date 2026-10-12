package ui

import (
	"testing"

	"github.com/ulrich/cfdo/internal/core"
)

func TestBackupRows(t *testing.T) {
	// core returns newest-first; the view must preserve that order.
	entries := []core.BackupEntry{
		{Dir: "backups/chat-2026-10-11", Script: "chat", Namespace: "abcdef1234567890", Class: "Room",
			CreatedAt: "2026-10-11T09:00:00Z", ObjectCount: 40, Failures: 0},
		{Dir: "backups/chat-2026-10-10", Script: "chat", Namespace: "abcdef1234567890", Class: "Room",
			CreatedAt: "2026-10-10T09:00:00Z", ObjectCount: 38, Failures: 2},
	}
	rows := BackupRows(entries)
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].Dir != "backups/chat-2026-10-11" {
		t.Errorf("order not preserved: first row Dir = %q", rows[0].Dir)
	}
	if rows[0].Created != "2026-10-11T09:00:00Z" {
		t.Errorf("Created = %q, want verbatim CreatedAt", rows[0].Created)
	}
	if rows[0].Namespace != "abcdef12…" {
		t.Errorf("Namespace = %q, want truncated", rows[0].Namespace)
	}
	if rows[0].Objects != "40" {
		t.Errorf("Objects = %q, want 40", rows[0].Objects)
	}
	if rows[0].Failures.State != StateMuted {
		t.Errorf("clean backup Failures state = %s, want muted", rows[0].Failures.State)
	}
	if rows[1].Failures.Value != "2" || rows[1].Failures.State != StateWarn {
		t.Errorf("failed backup Failures = {%q,%s}, want {2,warn}", rows[1].Failures.Value, rows[1].Failures.State)
	}
}

func TestBackupRows_Empty(t *testing.T) {
	if rows := BackupRows(nil); len(rows) != 0 {
		t.Errorf("nil entries → %d rows, want 0", len(rows))
	}
}
