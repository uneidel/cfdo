//go:build withgui

// This file is the ONLY part of the ui package that imports Fyne/GL. It compiles
// and links solely under `-tags withgui` + CGO on a host with the GL toolchain,
// which is not yet provisioned in the squad VMs (the final withgui gate). It is
// deliberately a minimal bootstrap that stands the window up over the view's
// narrow seam; the rich dashboard widgets (namespaces table, status cards, the
// backup/restore log pane) land in a follow-up ticket once the toolchain is in
// place and the view can be screenshot-verified — phone/desktop, light/dark — per
// the frontend check list. Until then this path is NOT link-verified.

package ui

import (
	"context"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

// Run opens the native cfdo dashboard window and blocks until it is closed or ctx
// is canceled. svc is the render-only seam onto core; the window never touches the
// CLI, HTTP, or secrets — it reads already-parsed, already-masked results.
func Run(ctx context.Context, svc Service) error {
	_ = svc // consumed by the dashboard widgets in the follow-up ticket

	a := app.New()
	w := a.NewWindow("cfdo — Durable Objects")
	w.Resize(fyne.NewSize(960, 640))
	w.SetContent(container.NewCenter(
		widget.NewLabel("cfdo dashboard — loading…"),
	))

	// Honor context cancellation (e.g. SIGINT relayed from the CLI) by closing the
	// window so Run returns instead of blocking on a dead parent.
	go func() {
		<-ctx.Done()
		w.Close()
	}()

	w.ShowAndRun()
	return ctx.Err()
}
