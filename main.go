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
)

const usage = `cfdo — Cloudflare Durable Objects CLI

Usage:
  cfdo <command> [flags]

Commands:
  init      Record your Cloudflare credentials in ~/.cfdo/settings.json
  create    Scaffold a new Durable Object project in a directory
  list      List every Durable Object namespace on the account
  upload    Upload the worker script (and DO migrations) to Cloudflare
  status    Show namespaces, object counts and deployment state
  backup    Dump every object's storage to a local directory
  restore   Load a backup directory back into the objects

Environment (each falls back to ~/.cfdo/settings.json, written by "cfdo init"):
  CLOUDFLARE_API_TOKEN    API token (Workers Scripts:Edit, Account:Read)  [required]
  CLOUDFLARE_ACCOUNT_ID   Account id; overrides account_id in cfdo.json
  CFDO_SECRET             Shared secret guarding the worker admin routes

Run "cfdo <command> -h" for the flags of a command.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	args := os.Args[2:]
	var err error
	switch os.Args[1] {
	case "init":
		err = cmdInit(ctx, args)
	case "create":
		err = cmdCreate(ctx, args)
	case "list":
		err = cmdList(ctx, args)
	case "upload":
		err = cmdUpload(ctx, args)
	case "status":
		err = cmdStatus(ctx, args)
	case "backup":
		err = cmdBackup(ctx, args)
	case "restore":
		err = cmdRestore(ctx, args)
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "cfdo: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "cfdo: interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "cfdo: "+err.Error())
		os.Exit(1)
	}
}
