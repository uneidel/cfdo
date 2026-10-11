//go:build withgui

package main

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/ulrich/cfdo/internal/core"
	"github.com/ulrich/cfdo/internal/gui"
)

// run is the desktop-build entry point. It is identical to the CLI tree but
// additionally mounts the `ui` command, which launches the native window.
//
// The GUI and its cgo/OpenGL/X11 dependencies are confined to this build tag
// and the internal/gui package, so the default `go build ./...` (and anyone
// cross-compiling the plain CLI) never needs a C toolchain or a display.
func run(ctx context.Context, args ...string) error {
	root := core.NewRootCmd()
	root.AddCommand(newUICmd())
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// newUICmd builds `cfdo ui`, the bridge between the cmd layer and the GUI:
// it constructs the shared core.Service and hands it to the view. gui.Run owns
// the window lifecycle and blocks until the user closes it (or ctx is
// cancelled), returning any fatal error.
//
// Contract expected of internal/gui (ui-developer, ticket ui-4):
//
//	func Run(ctx context.Context, svc core.Service) error
func newUICmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ui",
		Short: "Launch the native desktop app",
		Long: `ui launches the cfdo native desktop application.

It drives the same operations as the CLI through the shared core service;
all backups, restores and live inspection happen in-process.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return gui.Run(cmd.Context(), core.NewService())
		},
	}
}
