package ui

import (
	"context"
	"errors"
	"testing"

	"github.com/ulrich/cfdo/internal/core"
)

func TestConsole_LogStreamColoring(t *testing.T) {
	c := NewConsole()
	c.Apply(core.Event{Kind: core.EventLog, Stream: "stdout", Text: "uploading…"})
	st := c.Apply(core.Event{Kind: core.EventLog, Stream: "stderr", Text: "warn: retrying"})
	if len(st.Lines) != 2 {
		t.Fatalf("want 2 lines, got %d", len(st.Lines))
	}
	if st.Lines[0].Err {
		t.Error("stdout line should not be flagged Err")
	}
	if !st.Lines[1].Err {
		t.Error("stderr line should be flagged Err (red)")
	}
}

func TestConsole_ProgressMeterAndSpinner(t *testing.T) {
	c := NewConsole()
	// Total==0 ⇒ indeterminate spinner.
	st := c.Apply(core.Event{Kind: core.EventProgress, Done: 0, Total: 0})
	if !st.Meter.Visible || !st.Meter.Spinner {
		t.Errorf("Total==0 should show a visible spinner, got %+v", st.Meter)
	}
	if st.Meter.Label != "working…" {
		t.Errorf("spinner label = %q, want working…", st.Meter.Label)
	}
	// Determinate with failures.
	st = c.Apply(core.Event{Kind: core.EventProgress, Done: 12, Failed: 1, Total: 40})
	if st.Meter.Spinner {
		t.Error("Total>0 should not be a spinner")
	}
	if st.Meter.Label != "12/40 · 1 failed" {
		t.Errorf("meter label = %q, want 12/40 · 1 failed", st.Meter.Label)
	}
}

func TestConsole_TerminalPill(t *testing.T) {
	// Success.
	c := NewConsole()
	if c.State().Pill != PillRunning {
		t.Fatalf("fresh console pill = %s, want running", c.State().Pill)
	}
	st := c.Apply(core.Event{Kind: core.EventDone, Final: true})
	if st.Pill != PillSuccess || st.PillText != "done" {
		t.Errorf("success pill = {%s,%q}, want {success,done}", st.Pill, st.PillText)
	}
	if st.Active {
		t.Error("console should be inactive after terminal event")
	}
	// Failure carries the error text.
	c = NewConsole()
	st = c.Apply(core.Event{Kind: core.EventDone, Final: true, Err: errors.New("boom")})
	if st.Pill != PillError || st.PillText != "failed: boom" {
		t.Errorf("error pill = {%s,%q}, want {error,failed: boom}", st.Pill, st.PillText)
	}
}

// TestConsole_DrivenByFakeJob proves the reducer against a real *core.Job channel produced by a
// Service — ranging Events exactly as the Fyne widget layer will, with no display.
func TestConsole_DrivenByFakeJob(t *testing.T) {
	svc := &fakeService{jobScript: []core.Event{
		{Kind: core.EventLog, Stream: "stdout", Text: "starting backup"},
		{Kind: core.EventProgress, Done: 40, Total: 40},
		{Kind: core.EventLog, Stream: "stdout", Text: "wrote manifest"},
		{Kind: core.EventDone, Final: true},
	}}

	job, err := svc.Backup(context.Background(), core.BackupRequest{})
	if err != nil {
		t.Fatalf("Backup returned validation error: %v", err)
	}
	c := NewConsole()
	var last ConsoleState
	for e := range job.Events { // channel closes after the terminal event
		last = c.Apply(e)
	}
	if last.Active {
		t.Error("job should be inactive once Events closed")
	}
	if last.Pill != PillSuccess {
		t.Errorf("pill = %s, want success", last.Pill)
	}
	if last.Meter.Label != "40/40" {
		t.Errorf("meter = %q, want 40/40", last.Meter.Label)
	}
	if len(last.Lines) != 2 {
		t.Errorf("want 2 log lines, got %d", len(last.Lines))
	}
}

// TestConsole_ValidationErrorStartsNothing: a bad request comes back as the error with no Job.
func TestConsole_ValidationErrorStartsNothing(t *testing.T) {
	svc := &fakeService{jobErr: errors.New("missing --config")}
	job, err := svc.Restore(context.Background(), core.RestoreRequest{})
	if err == nil {
		t.Fatal("expected a validation error")
	}
	if job != nil {
		t.Error("no Job should be returned on validation error")
	}
}

// TestConsole_Cancel wires through to the Job's Cancel and records intent distinctly.
func TestConsole_Cancel(t *testing.T) {
	svc := &fakeService{jobScript: []core.Event{{Kind: core.EventDone, Final: true, Err: context.Canceled}}}
	job, err := svc.Upload(context.Background(), core.UploadRequest{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c := NewConsole()
	job.Cancel()
	st := c.MarkCancelled()
	if !st.Cancelled {
		t.Error("MarkCancelled should flag the state")
	}
	if !svc.cancelled {
		t.Error("Job.Cancel should have been invoked on the service")
	}
}
