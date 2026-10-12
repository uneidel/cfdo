//go:build withgui

package main

import (
	"context"

	"github.com/ulrich/cfdo/internal/core"
	"github.com/ulrich/cfdo/ui"
)

// run is the desktop-build entry point. It is identical to the CLI tree but
// additionally mounts the `ui` command, which launches the native window.
//
// The GUI and its cgo/OpenGL/X11 dependencies are confined to this build tag
// and the ui package, so the default `go build ./...` (and anyone
// cross-compiling the plain CLI) never needs a C toolchain or a display.
//
// The entire cmd<->view link is the single seam exposed by the ui package
// (ui-developer, ticket ui-3):
//
//	func NewCommand(svc core.Service) *cobra.Command
//
// We build the shared, transport-free core.Service here and inject it, so the
// view never constructs its own domain layer.
func run(ctx context.Context, args ...string) error {
	root := core.NewRootCmd()
	root.AddCommand(ui.NewCommand(core.NewService()))
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
