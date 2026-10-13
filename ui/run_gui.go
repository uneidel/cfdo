//go:build withgui

// This file is the ONLY part of the ui package that imports Fyne/GL. It compiles
// and links solely under `-tags withgui` + CGO on a host with the GL toolchain;
// the default no-cgo `go build ./...` never sees it (the !withgui run_stub.go
// supplies Run instead), so the plain CLI carries no GL/X11/cgo dependency.
//
// Everything here is thin glue: it renders the already-parsed, already-masked
// results from the pure, headless-tested view-models (namespaces_vm.go,
// status_vm.go, backups_vm.go, console.go) into Fyne widgets. No CLI, HTTP, SSE,
// token or secret is touched here — the window only reads through the ui.Service
// seam and folds job Events through the Console reducer.

package ui

import (
	"context"
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/ulrich/cfdo/internal/core"
)

// Run opens the native cfdo dashboard window and blocks until it is closed or ctx
// is canceled. svc is the render-only seam onto core; the window never touches the
// CLI, HTTP, or secrets — it reads already-parsed, already-masked results.
func Run(ctx context.Context, svc Service) error {
	a := app.New()
	w := a.NewWindow("cfdo — Durable Objects")
	w.Resize(fyne.NewSize(960, 680))

	d := &dashboard{svc: svc, win: w, ctx: ctx, bkSel: -1}
	w.SetContent(d.build())

	// Honor context cancellation (e.g. SIGINT relayed from the CLI) by closing the
	// window so Run returns instead of blocking on a dead parent.
	go func() {
		<-ctx.Done()
		fyne.Do(w.Close)
	}()

	// Populate the read panels once the window is up; reads run off the UI thread
	// and marshal their results back through fyne.Do.
	d.loadNamespaces()
	d.loadBackups()

	w.ShowAndRun()
	return ctx.Err()
}

// dashboard holds the live widgets and the view-model state behind them. All
// widget mutation happens on the Fyne UI goroutine (constructors, button taps,
// and work marshaled back via fyne.Do); the service reads and job streams run on
// their own goroutines and never touch a widget directly.
type dashboard struct {
	svc Service
	win fyne.Window
	ctx context.Context

	// Namespaces panel.
	nsRows  []NSRow
	nsDeep  bool
	nsTable *widget.Table

	// Status panel.
	statusScript *widget.Entry
	statusCards  *fyne.Container

	// Backups panel.
	bkRows  []BackupRow
	bkTable *widget.Table
	bkSel   int // selected row index, -1 = none

	// Shared job console (one job at a time; core serializes under a mutex).
	// jobPending guards the window between a click launching startJob and the
	// console going Active inside the deferred fyne.Do, so a rapid second click
	// cannot slip a second job past jobActive().
	console    *Console
	jobPending bool
	runningJob *core.Job
	logBox     *fyne.Container
	logScroll  *container.Scroll
	progress   *widget.ProgressBar
	spinner    *widget.ProgressBarInfinite
	meterLabel *widget.Label
	pill       *canvas.Text
	cancelBtn  *widget.Button
}

// build assembles the whole window: a tabbed set of read panels over a shared job
// log pane, split so both get real estate and the divider is draggable.
func (d *dashboard) build() fyne.CanvasObject {
	tabs := container.NewAppTabs(
		container.NewTabItem("Namespaces", d.buildNamespaces()),
		container.NewTabItem("Status", d.buildStatus()),
		container.NewTabItem("Backups", d.buildBackups()),
	)
	tabs.SetTabLocation(container.TabLocationTop)

	split := container.NewVSplit(tabs, d.buildLogPane())
	split.SetOffset(0.72)
	return split
}

// ---- namespaces panel --------------------------------------------------

var nsHeaders = []string{"Script", "Class", "Namespace", "SQLite", "Deployed", "Objects", "Modified"}

func (d *dashboard) buildNamespaces() fyne.CanvasObject {
	d.nsTable = widget.NewTable(
		func() (int, int) { return len(d.nsRows), len(nsHeaders) },
		func() fyne.CanvasObject { return canvas.NewText("", cellColor("")) },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			text, state, tip := d.nsCell(id.Row, id.Col)
			t.Text = text
			t.Color = cellColor(state)
			_ = tip // tooltips are not surfaced by canvas.Text; detail stays in the view-model
			t.Refresh()
		},
	)
	d.nsTable.ShowHeaderRow = true
	d.nsTable.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	d.nsTable.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		l := o.(*widget.Label)
		if id.Col >= 0 && id.Col < len(nsHeaders) {
			l.SetText(nsHeaders[id.Col])
		}
	}
	widths := []float32{140, 120, 110, 70, 90, 150, 100}
	for i, w := range widths {
		d.nsTable.SetColumnWidth(i, w)
	}

	deep := widget.NewCheck("Deep (verify object counts)", func(b bool) {
		d.nsDeep = b
		d.loadNamespaces()
	})
	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), d.loadNamespaces)
	bar := container.NewHBox(refresh, deep)
	return container.NewBorder(bar, nil, nil, nil, d.nsTable)
}

// nsCell returns the display text, color state, and tooltip detail for one cell.
func (d *dashboard) nsCell(row, col int) (string, CellState, string) {
	if row < 0 || row >= len(d.nsRows) {
		return "", "", ""
	}
	r := d.nsRows[row]
	switch col {
	case 0:
		return r.Script, "", ""
	case 1:
		return r.Class, "", ""
	case 2:
		return r.NSShort, StateMuted, r.NamespaceID
	case 3:
		return boolText(r.SQLite), "", ""
	case 4:
		return boolText(r.Deployed), stateFromBool(r.Deployed), ""
	case 5:
		return r.Objects.Text, r.Objects.State, r.Objects.Tooltip
	case 6:
		return r.Modified, StateMuted, ""
	}
	return "", "", ""
}

func (d *dashboard) loadNamespaces() {
	deep := d.nsDeep
	go func() {
		rows, err := d.svc.Namespaces(d.ctx, core.NamespacesRequest{Deep: deep})
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, d.win)
				return
			}
			d.nsRows = NamespaceRows(rows, deep, time.Now())
			d.nsTable.Refresh()
		})
	}()
}

// ---- status panel ------------------------------------------------------

func (d *dashboard) buildStatus() fyne.CanvasObject {
	d.statusScript = widget.NewEntry()
	d.statusScript.SetPlaceHolder("script name (blank = local project)")
	load := widget.NewButtonWithIcon("Load status", theme.SearchIcon(), func() {
		d.loadStatus(d.statusScript.Text)
	})
	d.statusScript.OnSubmitted = func(string) { d.loadStatus(d.statusScript.Text) }

	d.statusCards = container.NewVBox(widget.NewLabel("Enter a script (or leave blank for the local project) and load status."))
	bar := container.NewBorder(nil, nil, nil, load, d.statusScript)
	return container.NewBorder(bar, nil, nil, nil, container.NewVScroll(d.statusCards))
}

func (d *dashboard) loadStatus(script string) {
	go func() {
		rep, err := d.svc.Status(d.ctx, core.StatusRequest{Script: script})
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, d.win)
				return
			}
			d.statusCards.Objects = nil
			for _, card := range StatusCards(rep) {
				d.statusCards.Add(widget.NewCard(card.Title, "", fieldGrid(card.Fields)))
			}
			d.statusCards.Refresh()
		})
	}()
}

// fieldGrid lays a card's label/value fields into a two-column form grid, coloring
// the value by its CellState.
func fieldGrid(fields []Field) fyne.CanvasObject {
	grid := container.New(layout.NewFormLayout())
	for _, f := range fields {
		grid.Add(widget.NewLabelWithStyle(f.Label, fyne.TextAlignLeading, fyne.TextStyle{Bold: true}))
		v := canvas.NewText(f.Value, cellColor(f.State))
		v.Alignment = fyne.TextAlignLeading
		grid.Add(v)
	}
	return grid
}

// ---- backups panel -----------------------------------------------------

var bkHeaders = []string{"Script", "Namespace", "Class", "Created", "Objects", "Failures"}

func (d *dashboard) buildBackups() fyne.CanvasObject {
	d.bkTable = widget.NewTable(
		func() (int, int) { return len(d.bkRows), len(bkHeaders) },
		func() fyne.CanvasObject { return canvas.NewText("", cellColor("")) },
		func(id widget.TableCellID, o fyne.CanvasObject) {
			t := o.(*canvas.Text)
			text, state := d.bkCell(id.Row, id.Col)
			t.Text = text
			t.Color = cellColor(state)
			t.Refresh()
		},
	)
	d.bkTable.ShowHeaderRow = true
	d.bkTable.CreateHeader = func() fyne.CanvasObject {
		return widget.NewLabelWithStyle("", fyne.TextAlignLeading, fyne.TextStyle{Bold: true})
	}
	d.bkTable.UpdateHeader = func(id widget.TableCellID, o fyne.CanvasObject) {
		l := o.(*widget.Label)
		if id.Col >= 0 && id.Col < len(bkHeaders) {
			l.SetText(bkHeaders[id.Col])
		}
	}
	widths := []float32{140, 120, 120, 170, 90, 90}
	for i, w := range widths {
		d.bkTable.SetColumnWidth(i, w)
	}
	d.bkTable.OnSelected = func(id widget.TableCellID) { d.bkSel = id.Row }
	d.bkTable.OnUnselected = func(widget.TableCellID) { d.bkSel = -1 }

	refresh := widget.NewButtonWithIcon("Refresh", theme.ViewRefreshIcon(), d.loadBackups)
	backup := widget.NewButtonWithIcon("Back up now", theme.DownloadIcon(), d.doBackup)
	restore := widget.NewButtonWithIcon("Restore selected", theme.UploadIcon(), d.doRestore)
	restore.Importance = widget.DangerImportance
	bar := container.NewHBox(refresh, backup, restore)
	return container.NewBorder(bar, nil, nil, nil, d.bkTable)
}

func (d *dashboard) bkCell(row, col int) (string, CellState) {
	if row < 0 || row >= len(d.bkRows) {
		return "", ""
	}
	r := d.bkRows[row]
	switch col {
	case 0:
		return r.Script, ""
	case 1:
		return r.Namespace, StateMuted
	case 2:
		return r.Class, ""
	case 3:
		return r.Created, StateMuted
	case 4:
		return r.Objects, ""
	case 5:
		return r.Failures.Value, r.Failures.State
	}
	return "", ""
}

func (d *dashboard) loadBackups() {
	go func() {
		entries, err := d.svc.Backups(d.ctx)
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, d.win)
				return
			}
			d.bkRows = BackupRows(entries)
			d.bkSel = -1
			d.bkTable.UnselectAll()
			d.bkTable.Refresh()
		})
	}()
}

func (d *dashboard) doBackup() {
	if d.jobActive() {
		dialog.ShowInformation("Busy", "A job is already running — wait for it to finish.", d.win)
		return
	}
	d.startJob(func() (*core.Job, error) { return d.svc.Backup(d.ctx, core.BackupRequest{}) })
}

// doRestore gates the destructive restore behind an explicit confirmation naming
// the exact backup and its live target, per the frontend destructive-op rule.
func (d *dashboard) doRestore() {
	if d.bkSel < 0 || d.bkSel >= len(d.bkRows) {
		dialog.ShowInformation("No selection", "Select a backup row to restore first.", d.win)
		return
	}
	if d.jobActive() {
		dialog.ShowInformation("Busy", "A job is already running — wait for it to finish.", d.win)
		return
	}
	row := d.bkRows[d.bkSel]
	msg := fmt.Sprintf("Restore %s (namespace %s) from\n%s?\n\nThis writes objects back to the live namespace and cannot be undone.",
		row.Script, row.Namespace, row.Dir)
	dialog.ShowConfirm("Restore backup", msg, func(ok bool) {
		if !ok {
			return
		}
		d.startJob(func() (*core.Job, error) {
			return d.svc.Restore(d.ctx, core.RestoreRequest{Dir: row.Dir, Confirm: true})
		})
	}, d.win)
}

// ---- job log pane ------------------------------------------------------

func (d *dashboard) buildLogPane() fyne.CanvasObject {
	d.pill = canvas.NewText("idle", cellColor(StateMuted))
	d.pill.TextStyle = fyne.TextStyle{Bold: true}

	d.meterLabel = widget.NewLabel("")
	d.progress = widget.NewProgressBar()
	d.progress.Hide()
	d.spinner = widget.NewProgressBarInfinite()
	d.spinner.Hide()
	d.spinner.Stop()

	d.cancelBtn = widget.NewButtonWithIcon("Cancel", theme.CancelIcon(), d.cancelJob)
	d.cancelBtn.Disable()

	d.logBox = container.NewVBox()
	d.logScroll = container.NewVScroll(d.logBox)

	header := container.NewBorder(nil, nil, d.pill, d.cancelBtn,
		container.NewVBox(d.meterLabel, d.progress, d.spinner))
	return container.NewBorder(header, nil, nil, nil, d.logScroll)
}

// startJob begins a streaming action and folds its Events through the Console
// reducer, mirroring each state change into the log pane on the UI thread.
func (d *dashboard) startJob(start func() (*core.Job, error)) {
	// Set synchronously on the UI thread, before launching the goroutine, so a
	// second click arriving before the console goes Active is still refused.
	d.jobPending = true
	go func() {
		defer fyne.Do(func() { d.jobPending = false })
		job, err := start()
		fyne.Do(func() {
			if err != nil {
				dialog.ShowError(err, d.win)
				return
			}
			d.runningJob = job
			d.resetConsole()
			d.cancelBtn.Enable()
		})
		if err != nil || job == nil {
			return
		}
		for e := range job.Events {
			ev := e
			fyne.Do(func() { d.onEvent(ev) })
		}
	}()
}

// onEvent folds one Event into the Console and reflects the result into widgets.
// Runs on the UI thread (via fyne.Do), so it is the sole reader/writer of console.
func (d *dashboard) onEvent(e core.Event) {
	if d.console == nil {
		return
	}
	st := d.console.Apply(e)
	for i := len(d.logBox.Objects); i < len(st.Lines); i++ {
		ln := st.Lines[i]
		c := cellColor("")
		if ln.Err {
			c = cellColor(StateError)
		}
		d.logBox.Add(canvas.NewText(ln.Text, c))
	}
	d.logBox.Refresh()
	d.logScroll.ScrollToBottom()
	d.renderMeterPill(st)
}

func (d *dashboard) cancelJob() {
	if d.runningJob != nil {
		d.runningJob.Cancel()
	}
	if d.console != nil {
		d.renderMeterPill(d.console.MarkCancelled())
	}
}

func (d *dashboard) resetConsole() {
	d.console = NewConsole()
	d.logBox.Objects = nil
	d.logBox.Refresh()
	d.renderMeterPill(d.console.State())
}

// jobActive reports whether a job is currently running (so a second one is refused).
// It ORs the synchronous jobPending flag with the console's Active state to close the
// gap between a click and the console being created inside the deferred fyne.Do.
func (d *dashboard) jobActive() bool {
	return d.jobPending || (d.console != nil && d.console.State().Active)
}

// renderMeterPill reflects the console's meter and terminal pill into the widgets.
func (d *dashboard) renderMeterPill(st ConsoleState) {
	text, state := pillLabel(st)
	d.pill.Text = text
	d.pill.Color = cellColor(state)
	d.pill.Refresh()

	m := st.Meter
	switch {
	case !m.Visible:
		d.progress.Hide()
		d.spinner.Hide()
		d.spinner.Stop()
		d.meterLabel.SetText("")
	case m.Spinner:
		d.progress.Hide()
		d.spinner.Show()
		d.spinner.Start()
		d.meterLabel.SetText(m.Label)
	default:
		d.spinner.Hide()
		d.spinner.Stop()
		d.progress.Show()
		if m.Total > 0 {
			d.progress.SetValue(float64(m.Done) / float64(m.Total))
		}
		d.meterLabel.SetText(m.Label)
	}

	if st.Active {
		d.cancelBtn.Enable()
	} else {
		d.cancelBtn.Disable()
	}
}

// ---- shared helpers ----------------------------------------------------

// cellColor maps a view-model CellState to a theme color (which already differs
// per light/dark variant, so this stays correct in both).
func cellColor(s CellState) color.Color {
	switch s {
	case StateOK:
		return theme.Color(theme.ColorNameSuccess)
	case StateWarn:
		return theme.Color(theme.ColorNameWarning)
	case StateError:
		return theme.Color(theme.ColorNameError)
	case StateMuted:
		return theme.Color(theme.ColorNameDisabled)
	default:
		return theme.Color(theme.ColorNameForeground)
	}
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func stateFromBool(b bool) CellState {
	if b {
		return StateOK
	}
	return StateMuted
}
