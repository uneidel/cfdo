package ui

import (
	"fmt"

	"github.com/ulrich/cfdo/internal/core"
)

// The shared job console drives all five streaming actions (Backup/Restore/Upload/Create/Plugin).
// Only one job runs at a time (core serializes under a global mutex), so a single Console instance
// renders the active job. It is a pure reducer: feed it core.Events, read ConsoleState, render —
// no Fyne here, so the whole streaming contract is unit-tested headlessly.

// PillState is the terminal result badge of a job.
type PillState string

const (
	PillRunning PillState = "running" // in flight
	PillSuccess PillState = "success" // EventDone with Err == nil
	PillError   PillState = "error"   // EventDone with Err != nil
)

// LogLine is one streamed output line; Err marks a stderr line (rendered red).
type LogLine struct {
	Text string
	Err  bool
}

// Meter is the object-progress indicator. Spinner is true for indeterminate work (Total==0).
type Meter struct {
	Visible bool
	Spinner bool
	Done    int
	Failed  int
	Total   int
	Label   string // e.g. "12/40 · 1 failed", or "working…" while indeterminate
}

// ConsoleState is the full render state of the console at a point in time.
type ConsoleState struct {
	Lines     []LogLine
	Meter     Meter
	Pill      PillState
	PillText  string
	Active    bool // a job is running → the Cancel button is live
	Cancelled bool // user hit Cancel (surfaced distinctly from a plain failure)
}

// Console is the pure reducer for one job's lifetime.
type Console struct {
	state ConsoleState
}

// NewConsole opens a console for a freshly started job (actions return *Job already running).
func NewConsole() *Console {
	return &Console{state: ConsoleState{
		Lines:    []LogLine{},
		Pill:     PillRunning,
		PillText: "running…",
		Active:   true,
	}}
}

// State returns the current render state (value copy of the slice header is fine; callers read only).
func (c *Console) State() ConsoleState { return c.state }

// Apply folds one Event into the state and returns the updated snapshot. Events arrive in order;
// the terminal EventDone (Final=true) ends the job, after which core closes the channel.
func (c *Console) Apply(e core.Event) ConsoleState {
	switch e.Kind {
	case core.EventLog:
		c.state.Lines = append(c.state.Lines, LogLine{
			Text: e.Text,
			Err:  e.Stream == "stderr",
		})
	case core.EventProgress:
		c.state.Meter = Meter{
			Visible: true,
			Spinner: e.Total == 0,
			Done:    e.Done,
			Failed:  e.Failed,
			Total:   e.Total,
			Label:   meterLabel(e.Done, e.Failed, e.Total),
		}
	case core.EventDone:
		c.state.Active = false
		if e.Err != nil {
			c.state.Pill = PillError
			c.state.PillText = "failed: " + e.Err.Error()
		} else {
			c.state.Pill = PillSuccess
			c.state.PillText = "done"
		}
	}
	return c.state
}

// MarkCancelled records that the user requested cancellation. The job still ends via a terminal
// KindDone event (core returns the ctx error); this just lets the view distinguish intent.
func (c *Console) MarkCancelled() ConsoleState {
	c.state.Cancelled = true
	return c.state
}

// meterLabel formats the progress meter caption.
func meterLabel(done, failed, total int) string {
	if total == 0 { // indeterminate
		if done > 0 {
			return fmt.Sprintf("%d done", done)
		}
		return "working…"
	}
	label := fmt.Sprintf("%d/%d", done, total)
	if failed > 0 {
		label += fmt.Sprintf(" · %d failed", failed)
	}
	return label
}
