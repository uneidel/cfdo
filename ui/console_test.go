package ui

import (
	"context"
	"errors"
	"strings"
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

func TestConsole_FreshState(t *testing.T) {
	// A freshly opened console is live, running, with no lines, no meter, not cancelled.
	// Lines must be a non-nil empty slice so the widget layer can range it unconditionally.
	st := NewConsole().State()
	if st.Lines == nil || len(st.Lines) != 0 {
		t.Errorf("fresh Lines = %#v, want non-nil empty", st.Lines)
	}
	if !st.Active || st.Cancelled {
		t.Errorf("fresh flags = {active:%v cancelled:%v}, want {true,false}", st.Active, st.Cancelled)
	}
	if st.Pill != PillRunning || st.PillText != "running…" {
		t.Errorf("fresh pill = {%s,%q}, want {running,running…}", st.Pill, st.PillText)
	}
	if st.Meter.Visible {
		t.Error("fresh meter should not be visible before any progress event")
	}
}

func TestConsole_IndeterminateWithProgress(t *testing.T) {
	// Total==0 but Done>0: still a spinner (count unknown), but the label reports the
	// running tally instead of the generic "working…".
	st := NewConsole().Apply(core.Event{Kind: core.EventProgress, Done: 3, Total: 0})
	if !st.Meter.Spinner || !st.Meter.Visible {
		t.Errorf("Total==0 should stay an indeterminate spinner, got %+v", st.Meter)
	}
	if st.Meter.Label != "3 done" {
		t.Errorf("indeterminate label = %q, want '3 done'", st.Meter.Label)
	}
}

func TestConsole_ProgressLatestWins(t *testing.T) {
	// Each progress event fully replaces the meter — the view shows the latest tick,
	// not an accumulation.
	c := NewConsole()
	c.Apply(core.Event{Kind: core.EventProgress, Done: 10, Total: 40})
	st := c.Apply(core.Event{Kind: core.EventProgress, Done: 25, Failed: 2, Total: 40})
	if st.Meter.Done != 25 || st.Meter.Failed != 2 || st.Meter.Total != 40 {
		t.Errorf("meter = %+v, want latest {25,2,40}", st.Meter)
	}
	if st.Meter.Label != "25/40 · 2 failed" {
		t.Errorf("label = %q, want '25/40 · 2 failed'", st.Meter.Label)
	}
}

func TestConsole_ProgressDoesNotTouchLines(t *testing.T) {
	// Logs and progress are independent streams: a progress tick between two log lines
	// must not reorder or drop the log lines.
	c := NewConsole()
	c.Apply(core.Event{Kind: core.EventLog, Stream: "stdout", Text: "first"})
	c.Apply(core.Event{Kind: core.EventProgress, Done: 1, Total: 2})
	st := c.Apply(core.Event{Kind: core.EventLog, Stream: "stdout", Text: "second"})
	if len(st.Lines) != 2 || st.Lines[0].Text != "first" || st.Lines[1].Text != "second" {
		t.Errorf("lines = %+v, want [first second] in order", st.Lines)
	}
}

func TestConsole_CancelThenTerminalError(t *testing.T) {
	// Cancelling records intent; the job still ends via a terminal event carrying the
	// context error. The view must be able to tell "user cancelled" from a plain failure:
	// Cancelled stays true AND the pill reflects the terminal error text.
	c := NewConsole()
	c.MarkCancelled()
	st := c.Apply(core.Event{Kind: core.EventDone, Final: true, Err: context.Canceled})
	if !st.Cancelled {
		t.Error("Cancelled intent should survive the terminal event")
	}
	if st.Pill != PillError {
		t.Errorf("pill = %s, want error", st.Pill)
	}
	if st.Active {
		t.Error("console should be inactive after the terminal event")
	}
	if !strings.Contains(st.PillText, "context canceled") {
		t.Errorf("pill text = %q, want it to carry the context-cancel cause", st.PillText)
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
