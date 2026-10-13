// Package ui is the cfdo native desktop view (Fyne v2) — the thin render-only layer
// over the ratified core.Service seam (ui-1 v2, re-scope #122). Per the locked DAG
// cmd→ui→core it imports github.com/ulrich/cfdo/internal/core and nothing from cmd;
// core never imports ui. There is no HTTP/SSE/SPA/browser/token anywhere in here.
//
// The package is split into two layers:
//
//   - PURE view-models (this file + *_vm.go, console.go): map core read-results and job
//     Events into presentation structs. No Fyne imports → unit-testable with `go test`,
//     no display, no GL. This is where correctness is proven (headless, architect-approved).
//   - Fyne WIDGETS + the GL entrypoint (added once the cgo/GL toolchain is provisioned):
//     thin glue over these view-models; the app.New bootstrap sits behind a build tag so
//     software-render specimen builds never link the GL driver.
package ui

import (
	"fmt"
	"strconv"
	"time"
)

// itoa is a terse int→string for cell values.
func itoa(i int) string { return strconv.Itoa(i) }

// itoaPtr renders an optional count: "—" when the value is unknown (nil), else the number.
// core uses *int on StatusReport to distinguish "omitted" from a real zero.
func itoaPtr(i *int) string {
	if i == nil {
		return "—"
	}
	return strconv.Itoa(*i)
}

// CellState classifies a value cell so tables and cards color it consistently.
type CellState string

const (
	StateOK    CellState = "ok"    // deployed / counts reconcile → green
	StateWarn  CellState = "warn"  // drift / partial / mismatch → amber
	StateError CellState = "error" // hard error → red
	StateMuted CellState = "muted" // no data yet / needs Deep → tertiary
)

// pillLabel maps a console state to the result-badge text and color. It is the single
// place the cancel case is reconciled for the view: the reducer keeps the raw terminal
// error (so the cancel→terminal-error contract in console.go stays intact), while a
// deliberate user cancel is shown as amber "cancelled" rather than a red failure.
// Success and non-cancelled failures pass through unchanged. Pure (no Fyne) so it is
// unit-tested headlessly alongside the reducer.
func pillLabel(st ConsoleState) (string, CellState) {
	switch st.Pill {
	case PillSuccess:
		return st.PillText, StateOK
	case PillError:
		if st.Cancelled {
			return "cancelled", StateWarn
		}
		return st.PillText, StateError
	default: // PillRunning
		if st.Cancelled {
			return "cancelling…", StateWarn
		}
		return st.PillText, StateWarn
	}
}

// idTrim is how many leading chars of an id we show before the ellipsis in dense cells.
const idTrim = 8

// shortID truncates a long namespace/object id for a table; the full value stays available for copy.
func shortID(s string) string {
	if len(s) <= idTrim {
		return s
	}
	return s[:idTrim] + "…"
}

// relTime renders a compact relative age ("just now", "5m", "3h", "2d") from an RFC3339
// timestamp (core's NamespaceReport.ModifiedOn). "" for an empty value; the raw string is
// returned verbatim if it is non-empty but not RFC3339 (never hide data). `now` is injected
// so it is deterministic under test.
func relTime(ts string, now time.Time) string {
	if ts == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
