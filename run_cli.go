//go:build !withgui

package main

import (
	"context"

	"github.com/ulrich/cfdo/internal/core"
)

// run is the default (CLI-only) entry point: the plain cfdo command tree with
// no GUI. The desktop build (-tags withgui) replaces this with run in
// run_gui.go, which additionally wires the `cfdo ui` command.
func run(ctx context.Context, args ...string) error {
	return core.Execute(ctx, args...)
}
