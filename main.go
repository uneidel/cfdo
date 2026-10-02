// cfdo is a small CLI for operating Cloudflare Durable Objects:
// scaffolding a project, uploading the worker, inspecting live state,
// and backing up / restoring per-object storage.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "cfdo",
		Short: "Cloudflare Durable Objects CLI",
		Long: `cfdo — Cloudflare Durable Objects CLI

Environment (each falls back to ~/.cfdo/settings.json, written by "cfdo init"):
  CLOUDFLARE_API_TOKEN    API token (Workers Scripts:Edit, Account:Read)  [required]
  CLOUDFLARE_ACCOUNT_ID   Account id; overrides account_id in cfdo.json
  CFDO_SECRET             Shared secret guarding the worker admin routes`,
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	root.AddCommand(
		newInitCmd(),
		newCreateCmd(),
		newListCmd(),
		newUploadCmd(),
		newStatusCmd(),
		newBackupCmd(),
		newRestoreCmd(),
		newDeleteCmd(),
	)
	return root
}

// execute runs the CLI with args (excluding the program name).
func execute(ctx context.Context, args ...string) error {
	root := newRootCmd()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := execute(ctx, os.Args[1:]...); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cfdo: interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "cfdo: "+err.Error())
		os.Exit(1)
	}
}
