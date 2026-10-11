// cfdo is a small CLI for operating Cloudflare Durable Objects:
// scaffolding a project, uploading the worker, inspecting live state,
// and backing up / restoring per-object storage.
//
// All of its behaviour lives in the importable core package, so the same
// operations can drive both this CLI and the native desktop app.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]...); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cfdo: interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "cfdo: "+err.Error())
		os.Exit(1)
	}
}
