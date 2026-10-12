//go:build withgui

package main

import (
	"context"

	"github.com/ulrich/cfdo/internal/core"
	"github.com/ulrich/cfdo/ui"
)

// run is the desktop-build entry point: the plain cfdo tree plus the `cfdo ui`
// command, bound at the ratified seam ui.NewCommand(core.Service).
func run(ctx context.Context, args ...string) error {
	root := core.NewRootCmd()
	root.AddCommand(ui.NewCommand(core.NewService()))
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}
