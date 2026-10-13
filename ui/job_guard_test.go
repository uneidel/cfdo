//go:build withgui

package ui

import "testing"

// TestJobActivePendingGuard pins the ui-10 TOCTOU fix: jobActive() must report a job as
// in flight the instant startJob flips jobPending on the UI thread, before the console is
// created inside the deferred fyne.Do. core.runAction returns the Job immediately and only
// locks actionMu inside the goroutine, so without this synchronous flag two fast clicks both
// pass the guard and the second resetConsole wipes the first job's live log.
func TestJobActivePendingGuard(t *testing.T) {
	d := &dashboard{}
	if d.jobActive() {
		t.Fatal("fresh dashboard: jobActive must be false")
	}

	// The pending flag alone — set synchronously by startJob before the console exists —
	// must make the guard refuse a second job.
	d.jobPending = true
	if !d.jobActive() {
		t.Fatal("jobPending must make jobActive report true (closes the pre-console window)")
	}

	// And once the pending flag clears, an Active console still reports active (the existing
	// guard behavior is preserved, not replaced).
	d.jobPending = false
	if d.jobActive() {
		t.Fatal("cleared pending + nil console: jobActive must be false")
	}
	d.console = NewConsole()
	if !d.jobActive() {
		t.Fatal("an Active console must still report jobActive")
	}
}
