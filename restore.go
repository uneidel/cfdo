package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/spf13/cobra"
)

func newRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore <backup-dir>",
		Short: "Load a backup directory back into the objects",
		Long:  "Loads a backup directory back into the objects it came from.",
		Args:  cobra.ExactArgs(1),
	}
	fs := cmd.Flags()
	confPath := fs.StringP("config", "c", "", "path to cfdo.json (default: nearest one up the tree)")
	mode := fs.String("mode", "merge", "merge (write over existing keys) or replace (wipe the object first)")
	concurrency := fs.Int("concurrency", 4, "objects to import in parallel")
	only := fs.String("only", "", "restore just this object id")
	yes := fs.BoolP("yes", "y", false, "skip the confirmation prompt")
	skipVerify := fs.Bool("skip-verify", false, "do not check file checksums against the manifest")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		dir := args[0]
		if *mode != "merge" && *mode != "replace" {
			return fmt.Errorf("--mode must be merge or replace")
		}
		if *concurrency < 1 {
			return fmt.Errorf("--concurrency must be at least 1")
		}

		cfg, err := loadConfig(*confPath)
		if err != nil {
			return err
		}
		token, err := cfg.token()
		if err != nil {
			return err
		}
		secret, err := cfg.secret()
		if err != nil {
			return err
		}

		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			return fmt.Errorf("reading manifest: %w (is %s a cfdo backup directory?)", err, dir)
		}
		var man Manifest
		if err := json.Unmarshal(raw, &man); err != nil {
			return fmt.Errorf("parsing manifest: %w", err)
		}
		if man.Format != 1 {
			return fmt.Errorf("manifest format %d is not supported by this build", man.Format)
		}

		targets := man.Objects
		if *only != "" {
			targets = nil
			for _, o := range man.Objects {
				if o.ID == *only {
					targets = append(targets, o)
				}
			}
			if len(targets) == 0 {
				return fmt.Errorf("object %s is not in this backup", *only)
			}
		}

		if man.Class != cfg.ClassName || man.Script != cfg.ScriptName {
			fmt.Fprintf(os.Stderr, "warning: backup is from %s/%s but cfdo.json points at %s/%s\n",
				man.Script, man.Class, cfg.ScriptName, cfg.ClassName)
		}

		client := NewClient(token)
		sub, _ := client.WorkersDevSubdomain(ctx, cfg.AccountID)
		workerURL, err := cfg.resolveWorkerURL(sub)
		if err != nil {
			return err
		}
		admin := newAdminClient(workerURL, secret)
		if _, err := admin.Ping(ctx); err != nil {
			return fmt.Errorf("admin route check failed: %w", err)
		}

		fmt.Printf("Restore %d object(s) from %s\n", len(targets), dir)
		fmt.Printf("  into   %s/%s via %s\n", cfg.ScriptName, cfg.ClassName, workerURL)
		fmt.Printf("  mode   %s\n", *mode)
		if *mode == "replace" {
			fmt.Println("  replace deletes all current storage in each target object first.")
		}
		if !*yes {
			ok, err := confirm("Proceed? [y/N] ")
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("aborted")
			}
		}

		var (
			wg       sync.WaitGroup
			sem      = make(chan struct{}, *concurrency)
			done     atomic.Int64
			kvTotal  atomic.Int64
			rowTotal atomic.Int64
			mu       sync.Mutex
			failures []ObjectFail
		)

		for _, o := range targets {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
			}
			if ctx.Err() != nil {
				break
			}
			wg.Add(1)
			go func(o ObjectMeta) {
				defer wg.Done()
				defer func() { <-sem }()

				body, err := os.ReadFile(filepath.Join(dir, o.File))
				if err == nil && !*skipVerify && o.SHA256 != "" {
					sum := sha256.Sum256(body)
					if hex.EncodeToString(sum[:]) != o.SHA256 {
						err = fmt.Errorf("checksum mismatch — %s has been modified since the backup", o.File)
					}
				}
				var res *importResult
				if err == nil {
					res, err = admin.Import(ctx, o.ID, *mode, body)
				}
				if err != nil {
					mu.Lock()
					failures = append(failures, ObjectFail{ID: o.ID, Error: err.Error()})
					mu.Unlock()
					fmt.Fprintf(os.Stderr, "  ! %s: %v\n", shorten(o.ID, 16), err)
					return
				}
				kvTotal.Add(int64(res.KV))
				rowTotal.Add(int64(res.Rows))
				if n := done.Add(1); n%10 == 0 || int(n) == len(targets) {
					fmt.Printf("\r  %d/%d objects", n, len(targets))
				}
			}(o)
		}
		wg.Wait()
		fmt.Println()

		if err := ctx.Err(); err != nil {
			return err
		}
		fmt.Printf("Restored %d/%d object(s): %d key(s), %d SQL row(s)\n",
			done.Load(), len(targets), kvTotal.Load(), rowTotal.Load())
		if len(failures) > 0 {
			return fmt.Errorf("%d object(s) failed to restore", len(failures))
		}
		return nil
	}
	return cmd
}

func confirm(prompt string) (bool, error) {
	fmt.Print(prompt)
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		return false, nil
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}
