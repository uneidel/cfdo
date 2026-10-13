//go:build withgui

// Screenshot specimen harness (withgui only). It software-renders the real Fyne
// dashboard (run_gui.go) over the fakeService at phone and desktop widths in both
// theme variants, writing PNGs to $CFDO_SHOT_DIR. It uses Fyne's software test
// canvas — no GL driver, no display — so it exercises the actual widget tree and
// view-model glue without an app.New() window. It is skipped unless CFDO_SHOT_DIR
// is set, so `go test -tags withgui ./...` stays side-effect free by default.
package ui

import (
	"context"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/ulrich/cfdo/internal/core"
)

// variantTheme wraps the default theme to force a single light/dark variant,
// regardless of the variant Fyne would otherwise pass. This lets one process
// render both variants deterministically.
type variantTheme struct {
	fyne.Theme
	v fyne.ThemeVariant
}

func (t variantTheme) Color(n fyne.ThemeColorName, _ fyne.ThemeVariant) color.Color {
	return t.Theme.Color(n, t.v)
}

// specimenNow is a fixed clock so relative times in the specimen render deterministically.
func specimenNow() time.Time { return time.Date(2026, 10, 13, 10, 30, 0, 0, time.UTC) }

// specimenService returns a fakeService seeded with representative data covering
// the states the view-models color differently (deployed/not, drift, errors).
func specimenService() *fakeService {
	idx3 := 3
	idx0 := 0
	sq := true
	nsq := false
	oc := 42
	owd := 40
	io := 42
	return &fakeService{
		namespaces: []core.NamespaceReport{
			{Script: "chat-room", Class: "ChatRoom", NamespaceID: "a1b2c3d4e5f6a7b8", SQLite: true, Deployed: true, ModifiedOn: "2026-10-13T07:30:00Z", APIObjects: 42, IndexObjects: &idx3},
			{Script: "presence", Class: "Presence", NamespaceID: "99aa88bb77cc66dd", SQLite: false, Deployed: true, ModifiedOn: "2026-10-11T09:00:00Z", APIObjects: 7},
			{Script: "rate-limit", Class: "RateLimiter", NamespaceID: "deadbeefcafef00d", SQLite: true, Deployed: false, APIObjects: 0, IndexObjects: &idx0},
			{Script: "counters", Class: "Counter", NamespaceID: "0011223344556677", SQLite: true, Deployed: true, ModifiedOn: "2026-10-13T10:15:00Z", APIObjects: 128, APIError: "rate limited (429)"},
		},
		backups: []core.BackupEntry{
			{Dir: "backups/chat-room-20261013-0930", Script: "chat-room", Namespace: "a1b2c3d4", Class: "ChatRoom", CreatedAt: "2026-10-13T09:30:00Z", ObjectCount: 42, Failures: 0},
			{Dir: "backups/presence-20261012-1800", Script: "presence", Namespace: "99aa88bb", Class: "Presence", CreatedAt: "2026-10-12T18:00:00Z", ObjectCount: 7, Failures: 0},
			{Dir: "backups/counters-20261011-2200", Script: "counters", Namespace: "00112233", Class: "Counter", CreatedAt: "2026-10-11T22:00:00Z", ObjectCount: 120, Failures: 3},
		},
		status: core.StatusReport{
			Script: "chat-room", AccountID: "f00dcafef00dcafef00dcafef00dcafe", Class: "ChatRoom", Binding: "CHAT_ROOM",
			SQLite: &sq, Deployed: true, ModifiedOn: "2026-10-13T07:30:00Z", CompatibilityDate: "2026-09-01",
			LocalMigrationTag: "v5", AppliedMigrationTag: "v5", RemoteMigrationTag: "v4",
			NamespaceID: "a1b2c3d4e5f6a7b8", NamespaceSQLite: &nsq, ObjectCount: &oc, ObjectsWithData: &owd, IndexedObjects: &io,
			WorkerURL: "https://chat-room.example.workers.dev",
			Extra:     map[string]string{"region": "weur"},
		},
	}
}

// specimenLog is a short scripted job stream so the console pane renders populated
// (a few log lines, a progress tick, then a terminal done event).
var specimenLog = []core.Event{
	{Kind: core.EventLog, Stream: "stdout", Text: "backing up chat-room (namespace a1b2c3d4)…"},
	{Kind: core.EventLog, Stream: "stdout", Text: "exporting object ac91… (1/42)"},
	{Kind: core.EventProgress, Done: 21, Total: 42},
	{Kind: core.EventLog, Stream: "stderr", Text: "warning: object 7f2e… returned no storage, skipped"},
	{Kind: core.EventProgress, Done: 42, Total: 42},
	{Kind: core.EventDone, Final: true},
}

// buildSpecimen constructs a fully-populated dashboard for a given tab and renders
// it the way Run would, but synchronously (no goroutines/fyne.Do): read panels are
// filled directly, the console is folded through its real reducer.
func buildSpecimen(tabIndex int) fyne.CanvasObject {
	svc := specimenService()
	d := &dashboard{svc: svc, bkSel: -1}
	root := d.build()

	// Populate the namespaces + backups tables directly (Run does this via async reads).
	nsRows, _ := svc.Namespaces(context.Background(), core.NamespacesRequest{Deep: true})
	d.nsRows = NamespaceRows(nsRows, true, specimenNow())
	d.nsTable.Refresh()
	bkEntries, _ := svc.Backups(context.Background())
	d.bkRows = BackupRows(bkEntries)
	d.bkTable.Refresh()

	// Populate the status cards (replicates loadStatus' body synchronously).
	rep, _ := svc.Status(context.Background(), core.StatusRequest{Script: "chat-room"})
	d.statusCards.Objects = nil
	for _, card := range StatusCards(rep) {
		d.statusCards.Add(widget.NewCard(card.Title, "", fieldGrid(card.Fields)))
	}
	d.statusCards.Refresh()

	// Fold the scripted job stream through the real console reducer into the log pane.
	d.resetConsole()
	for _, e := range specimenLog {
		d.onEvent(e)
	}

	// Select the requested read tab so each specimen highlights a different panel.
	if split, ok := root.(*container.Split); ok {
		if at, ok := split.Leading.(*container.AppTabs); ok {
			at.SelectIndex(tabIndex)
		}
	}
	return root
}

func TestScreenshotSpecimens(t *testing.T) {
	dir := os.Getenv("CFDO_SHOT_DIR")
	if dir == "" {
		t.Skip("set CFDO_SHOT_DIR to render dashboard screenshot specimens")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	variants := []struct {
		name string
		v    fyne.ThemeVariant
	}{
		{"light", theme.VariantLight},
		{"dark", theme.VariantDark},
	}
	sizes := []struct {
		name string
		w, h float32
	}{
		{"phone", 390, 780},
		{"desktop", 1040, 720},
	}
	tabs := []struct {
		name string
		idx  int
	}{
		{"namespaces", 0},
		{"status", 1},
		{"backups", 2},
	}

	a := test.NewApp()
	for _, vr := range variants {
		a.Settings().SetTheme(variantTheme{theme.DefaultTheme(), vr.v})
		for _, sz := range sizes {
			for _, tb := range tabs {
				content := buildSpecimen(tb.idx)
				w := test.NewWindow(content)
				w.SetPadded(false)
				w.Resize(fyne.NewSize(sz.w, sz.h))
				img := w.Canvas().Capture()
				name := filepath.Join(dir, tb.name+"-"+sz.name+"-"+vr.name+".png")
				f, err := os.Create(name)
				if err != nil {
					t.Fatal(err)
				}
				if err := png.Encode(f, img); err != nil {
					f.Close()
					t.Fatal(err)
				}
				f.Close()
				w.Close()
				t.Logf("wrote %s (%dx%d)", name, img.Bounds().Dx(), img.Bounds().Dy())
			}
		}
	}
}
