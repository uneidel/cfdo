package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestServiceBackups(t *testing.T) {
	dir := t.TempDir()
	bk := filepath.Join(dir, "backups", "app-20260101T000000Z")
	if err := os.MkdirAll(bk, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"format":1,"created_at":"2026-01-01T00:00:00Z","script":"app","class":"Thing",` +
		`"namespace_id":"ns-1","objects":[{"id":"a"},{"id":"b"}],"failed":[{"id":"c","error":"x"}]}`
	if err := os.WriteFile(filepath.Join(bk, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	// A stray non-backup directory must be ignored, not error.
	if err := os.MkdirAll(filepath.Join(dir, "backups", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	got, err := NewService().Backups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 backup, got %d: %+v", len(got), got)
	}
	e := got[0]
	if e.Script != "app" || e.Namespace != "ns-1" || e.Class != "Thing" {
		t.Errorf("wrong fields: %+v", e)
	}
	if e.ObjectCount != 2 || e.Failures != 1 {
		t.Errorf("want 2 objects / 1 failure, got %d / %d", e.ObjectCount, e.Failures)
	}
}

func TestServiceBackupsMissingRoot(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := NewService().Backups(context.Background())
	if err != nil {
		t.Fatalf("missing backups dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestServicePluginsOutsideProject(t *testing.T) {
	t.Chdir(t.TempDir())
	got, err := NewService().Plugins(context.Background())
	if err != nil {
		t.Fatalf("outside a project should be empty, not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}

func TestServiceDeleteConfirmGuard(t *testing.T) {
	svc := NewService()
	if err := svc.Delete(context.Background(), DeleteRequest{Script: "app", ConfirmName: "wrong"}); err == nil {
		t.Fatal("expected error when confirm_name != script")
	}
	if err := svc.Delete(context.Background(), DeleteRequest{Script: "", ConfirmName: ""}); err == nil {
		t.Fatal("expected error when script is empty")
	}
}

func TestServiceRestoreValidation(t *testing.T) {
	svc := NewService()
	if _, err := svc.Restore(context.Background(), RestoreRequest{Confirm: true}); err == nil {
		t.Fatal("expected error when dir is empty")
	}
	if _, err := svc.Restore(context.Background(), RestoreRequest{Dir: "x"}); err == nil {
		t.Fatal("expected error when confirm is false")
	}
	if _, err := svc.Restore(context.Background(), RestoreRequest{Dir: "x", Mode: "bogus", Confirm: true}); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

// TestServiceJobPlumbing drives a real action through runAction (stdout swap,
// scan goroutines, channel streaming, terminal event) with no network: a
// restore from a temp dir with no cfdo.json fails fast in loadConfig. It proves
// Events streams, the terminal event carries Done=true + the error, and the
// channel then closes.
func TestServiceJobPlumbing(t *testing.T) {
	t.Chdir(t.TempDir())
	job, err := NewService().Restore(context.Background(), RestoreRequest{Dir: "nope", Confirm: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	var terminal *Event
	for ev := range job.Events {
		if ev.Final {
			e := ev
			terminal = &e
		}
	}
	if terminal == nil {
		t.Fatal("expected a terminal (Final) event before the channel closed")
	}
	if terminal.Err == nil {
		t.Fatal("expected restore to fail without a project")
	}
}
