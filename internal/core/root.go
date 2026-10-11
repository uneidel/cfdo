package core

import (
	"context"

	"github.com/spf13/cobra"
)

// NewRootCmd builds the cfdo command tree. The desktop app wires its own `ui`
// command onto this (in package main, to keep the GUI/cgo deps out of core).
func NewRootCmd() *cobra.Command {
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
		newPluginCmd(),
	)
	return root
}

// execute runs the CLI with args (excluding the program name).
func execute(ctx context.Context, args ...string) error {
	root := NewRootCmd()
	root.SetArgs(args)
	return root.ExecuteContext(ctx)
}

// Execute runs the cfdo CLI with args (excluding the program name). It is the
// entry point the thin main package calls.
func Execute(ctx context.Context, args ...string) error {
	return execute(ctx, args...)
}
