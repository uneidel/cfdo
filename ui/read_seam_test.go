package ui

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ulrich/cfdo/internal/core"
)

// The read view-models (StatusCards, NamespaceRows, BackupRows) are pure, but the widget
// layer reaches them through the Service seam: fetch → on error show the error, else map
// the result. These tests drive that fetch→map flow against the fake, exercising BOTH the
// happy path (result flows into the view-model) and the error path (the view-model is never
// built, the error is surfaced) — the contract the Fyne layer relies on but cannot prove
// headlessly.

func TestSeam_NamespacesSuccessAndError(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	svc := &fakeService{namespaces: []core.NamespaceReport{
		{Script: "chat", NamespaceID: "abcdef1234567890", APIObjects: 7, IndexObjects: intp(7)},
	}}
	got, err := svc.Namespaces(ctx, core.NamespacesRequest{Deep: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := NamespaceRows(got, true, now)
	if len(rows) != 1 || rows[0].Objects.State != StateOK {
		t.Fatalf("mapped rows = %+v, want 1 reconciled row", rows)
	}

	// Error path: the read fails, so nothing is mapped and the error reaches the caller.
	boom := errors.New("403 forbidden")
	failing := &fakeService{readErr: boom}
	if _, err := failing.Namespaces(ctx, core.NamespacesRequest{}); !errors.Is(err, boom) {
		t.Errorf("Namespaces error = %v, want %v propagated", err, boom)
	}
}

func TestSeam_StatusSuccessAndError(t *testing.T) {
	ctx := context.Background()

	svc := &fakeService{status: core.StatusReport{Script: "chat", Deployed: true}}
	rep, err := svc.Status(ctx, core.StatusRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cards := StatusCards(rep)
	if ov, ok := cardByTitle(cards, "Overview"); !ok {
		t.Error("expected an Overview card from a successful status read")
	} else if f, _ := fieldByLabel(ov, "Script"); f.Value != "chat" {
		t.Errorf("Script card = %q, want chat", f.Value)
	}

	boom := errors.New("no cfdo.json")
	failing := &fakeService{readErr: boom}
	if _, err := failing.Status(ctx, core.StatusRequest{}); !errors.Is(err, boom) {
		t.Errorf("Status error = %v, want %v propagated", err, boom)
	}
}

func TestSeam_BackupsSuccessAndError(t *testing.T) {
	ctx := context.Background()

	svc := &fakeService{backups: []core.BackupEntry{
		{Dir: "backups/chat-1", Script: "chat", CreatedAt: "2026-10-11T09:00:00Z", ObjectCount: 3, Failures: 1},
	}}
	got, err := svc.Backups(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rows := BackupRows(got)
	if len(rows) != 1 || rows[0].Failures.State != StateWarn {
		t.Fatalf("mapped backup rows = %+v, want 1 row flagged warn", rows)
	}

	boom := errors.New("permission denied")
	failing := &fakeService{readErr: boom}
	if _, err := failing.Backups(ctx); !errors.Is(err, boom) {
		t.Errorf("Backups error = %v, want %v propagated", err, boom)
	}
}

// TestSeam_DeleteSynchronous covers the one synchronous mutating action: it returns an
// error (or nil) directly, with no Job to stream — distinct from the five streaming actions.
func TestSeam_DeleteSynchronous(t *testing.T) {
	ctx := context.Background()
	if err := (&fakeService{}).Delete(ctx, core.DeleteRequest{}); err != nil {
		t.Errorf("clean Delete = %v, want nil", err)
	}
	boom := errors.New("object in use")
	if err := (&fakeService{deleteErr: boom}).Delete(ctx, core.DeleteRequest{}); !errors.Is(err, boom) {
		t.Errorf("Delete error = %v, want %v propagated", err, boom)
	}
}

// TestSeam_AllStreamingActionsStartAndCancel drives every streaming action (not just the
// one or two the console tests happen to use) through the fake, proving each returns a Job
// whose Events the reducer consumes to a success pill and whose Cancel reaches the service.
func TestSeam_AllStreamingActionsStartAndCancel(t *testing.T) {
	ctx := context.Background()
	script := []core.Event{
		{Kind: core.EventLog, Stream: "stdout", Text: "working"},
		{Kind: core.EventDone, Final: true},
	}
	actions := map[string]func(*fakeService) (*core.Job, error){
		"Backup":  func(s *fakeService) (*core.Job, error) { return s.Backup(ctx, core.BackupRequest{}) },
		"Restore": func(s *fakeService) (*core.Job, error) { return s.Restore(ctx, core.RestoreRequest{}) },
		"Upload":  func(s *fakeService) (*core.Job, error) { return s.Upload(ctx, core.UploadRequest{}) },
		"Create":  func(s *fakeService) (*core.Job, error) { return s.Create(ctx, core.CreateRequest{}) },
		"Plugin":  func(s *fakeService) (*core.Job, error) { return s.Plugin(ctx, core.PluginRequest{}) },
	}
	for name, start := range actions {
		t.Run(name, func(t *testing.T) {
			svc := &fakeService{jobScript: script}
			job, err := start(svc)
			if err != nil {
				t.Fatalf("%s returned validation error: %v", name, err)
			}
			c := NewConsole()
			var last ConsoleState
			for e := range job.Events {
				last = c.Apply(e)
			}
			if last.Pill != PillSuccess || last.Active {
				t.Errorf("%s final state = {%s,active:%v}, want {success,false}", name, last.Pill, last.Active)
			}
			job.Cancel()
			if !svc.cancelled {
				t.Errorf("%s Job.Cancel did not reach the service", name)
			}
		})
	}
}
