//go:build !withgui

package ui

import (
	"context"
	"errors"
)

// Run is the no-display build of the view entry point. The real Fyne window is
// compiled only under `-tags withgui` (run_gui.go); without that tag the binary
// carries no GL/CGO dependency, so invoking the gui command returns a clear
// instruction instead of failing to link a renderer that isn't there.
//
// Keeping this signature identical to the withgui build is what lets NewCommand
// compile CGO-off while still resolving Run.
func Run(_ context.Context, _ Service) error {
	return errors.New("cfdo was built without GUI support; rebuild with -tags withgui (requires the cgo/GL toolchain)")
}
