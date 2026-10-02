package main

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type migrations struct {
	OldTag           string       `json:"old_tag,omitempty"`
	NewTag           string       `json:"new_tag"`
	NewClasses       []string     `json:"new_classes,omitempty"`
	NewSQLiteClasses []string     `json:"new_sqlite_classes,omitempty"`
	DeletedClasses   []string     `json:"deleted_classes,omitempty"`
	RenamedClasses   []renamePair `json:"renamed_classes,omitempty"`
}

type renamePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func newUploadCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload the worker script (and DO migrations) to Cloudflare",
		Long:  "Uploads the worker module and the static assets directory, and applies any pending Durable Object migration.",
		Args:  cobra.NoArgs,
	}
	fs := cmd.Flags()
	confPath := fs.StringP("config", "c", "", "path to cfdo.json (default: nearest one up the tree)")
	dryRun := fs.Bool("dry-run", false, "print the upload metadata instead of sending it")
	tag := fs.String("tag", "", "migration tag to apply (default: migration_tag from cfdo.json)")
	newClasses := fs.StringArray("new-class", nil, "additional class to create in this migration (repeatable)")
	deletedClasses := fs.StringArray("deleted-class", nil, "class to delete in this migration — destroys its objects (repeatable)")
	renamedClasses := fs.StringArray("renamed-class", nil, "class rename as old=new (repeatable)")
	noAssets := fs.Bool("no-assets", false, "skip the assets directory; the deployed worker then serves no static files")
	noSecret := fs.Bool("no-secret", false, "do not bind CFDO_SECRET (backup/restore will stop working)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		cfg, err := loadConfig(*confPath)
		if err != nil {
			return err
		}
		state, err := cfg.loadState()
		if err != nil {
			return err
		}

		modules, err := collectModules(cfg.dir, cfg.MainModule)
		if err != nil {
			return err
		}

		var assets []assetFile
		if cfg.Assets != "" && !*noAssets {
			assets, err = scanAssets(filepath.Join(cfg.dir, cfg.Assets))
			if err != nil {
				return err
			}
		}

		targetTag := cfg.MigrationTag
		if *tag != "" {
			targetTag = *tag
		}

		bindings := []map[string]any{{
			"type":       "durable_object_namespace",
			"name":       cfg.Binding,
			"class_name": cfg.ClassName,
		}}
		secret := ""
		if !*noSecret {
			secret, err = cfg.secret()
			if err != nil {
				return fmt.Errorf("%w\n  (generate one with `openssl rand -base64 32`, or pass --no-secret)", err)
			}
			bindings = append(bindings, map[string]any{
				"type": "secret_text",
				"name": "CFDO_SECRET",
				"text": secret,
			})
		}

		if assets != nil {
			if cfg.Binding == "ASSETS" {
				return fmt.Errorf("binding name ASSETS is reserved for the static assets binding; rename %s's binding in %s", cfg.ClassName, configName)
			}
			bindings = append(bindings, map[string]any{"type": "assets", "name": "ASSETS"})
		}

		metadata := map[string]any{
			"main_module":        cfg.MainModule,
			"compatibility_date": cfg.CompatibilityDate,
			"bindings":           bindings,
		}
		// Static assets are matched first; anything without a file falls
		// through to the worker, so API and admin routes keep working.
		assetsMeta := map[string]any{
			"jwt": "<from asset upload session>",
			"config": map[string]any{
				"html_handling":      "auto-trailing-slash",
				"not_found_handling": "none",
			},
		}
		if assets != nil {
			metadata["assets"] = assetsMeta
		}

		mig, reason := planMigration(cfg, state, targetTag, *newClasses, *deletedClasses, *renamedClasses)
		if mig != nil {
			metadata["migrations"] = mig
		}

		if *dryRun {
			redacted, err := json.MarshalIndent(redactSecrets(metadata), "", "  ")
			if err != nil {
				return err
			}
			fmt.Printf("PUT /accounts/%s/workers/scripts/%s\n\n%s\n\nmigration: %s\nmodules: %d, %d bytes\n",
				cfg.AccountID, cfg.ScriptName, redacted, reason, len(modules), modulesSize(modules))
			for _, m := range modules {
				fmt.Printf("  %s (%s, %d bytes)\n", m.Name, m.ContentType, len(m.Data))
			}
			if assets != nil {
				fmt.Printf("assets: %d files, %d bytes from %s/\n", len(assets), assetsTotalSize(assets), cfg.Assets)
				for _, a := range assets {
					fmt.Printf("  %s (%d bytes)\n", a.Path, a.Size)
				}
			}
			return nil
		}

		token, err := cfg.token()
		if err != nil {
			return err
		}
		client := NewClient(token)

		if assets != nil {
			jwt, n, err := client.UploadAssets(ctx, cfg.AccountID, cfg.ScriptName, assets)
			if err != nil {
				return err
			}
			assetsMeta["jwt"] = jwt
			fmt.Printf("Assets: %d files from %s/ (%d new, rest unchanged)\n", len(assets), cfg.Assets, n)
		}

		body, err := json.MarshalIndent(metadata, "", "  ")
		if err != nil {
			return err
		}

		fmt.Printf("Uploading %s (%d modules, %d bytes) — %s\n", cfg.ScriptName, len(modules), modulesSize(modules), reason)
		for _, m := range modules[1:] {
			fmt.Printf("  + %s (%d bytes)\n", m.Name, len(m.Data))
		}
		info, err := client.UploadScript(ctx, cfg.AccountID, cfg.ScriptName, body, modules)
		if err != nil {
			return annotateUploadError(err, cfg, state, targetTag)
		}

		state.AppliedMigrationTag = targetTag
		state.LastUploadedAt = time.Now().UTC().Format(time.RFC3339)
		if err := cfg.saveState(state); err != nil {
			return fmt.Errorf("uploaded, but saving %s failed: %w", cfg.statePath(), err)
		}

		fmt.Printf("Uploaded. etag %s, modified %s\n", shorten(info.Etag, 16), info.ModifiedOn)

		if cfg.WorkersDev {
			if err := client.SetWorkersDev(ctx, cfg.AccountID, cfg.ScriptName, true); err != nil {
				fmt.Fprintf(os.Stderr, "warning: could not enable workers.dev: %v\n", err)
			}
		}
		sub, err := client.WorkersDevSubdomain(ctx, cfg.AccountID)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read workers.dev subdomain: %v\n", err)
		}
		if url, err := cfg.resolveWorkerURL(sub); err == nil {
			fmt.Printf("Worker URL: %s\n", url)
		}
		return nil
	}
	return cmd
}

// planMigration works out the old_tag/new_tag pair from what we last pushed.
// Cloudflare rejects a migration that re-creates an existing class, so the
// class list is only sent on the very first upload or when asked for.
func planMigration(cfg *Config, state *State, targetTag string, newClasses, deleted, renamed []string) (*migrations, string) {
	explicit := len(newClasses) > 0 || len(deleted) > 0 || len(renamed) > 0
	first := state.AppliedMigrationTag == ""

	if !first && state.AppliedMigrationTag == targetTag && !explicit {
		return nil, fmt.Sprintf("no migration (tag %s already applied)", targetTag)
	}

	m := &migrations{NewTag: targetTag}
	if !first {
		m.OldTag = state.AppliedMigrationTag
	}

	classes := append([]string(nil), newClasses...)
	if first {
		classes = append([]string{cfg.ClassName}, classes...)
	}
	if cfg.SQLite {
		m.NewSQLiteClasses = classes
	} else {
		m.NewClasses = classes
	}
	m.DeletedClasses = deleted
	for _, r := range renamed {
		from, to, ok := strings.Cut(r, "=")
		if !ok {
			continue
		}
		m.RenamedClasses = append(m.RenamedClasses, renamePair{From: from, To: to})
	}

	switch {
	case first:
		return m, fmt.Sprintf("creating %s class %s at tag %s", storageKind(cfg.SQLite), cfg.ClassName, targetTag)
	default:
		return m, fmt.Sprintf("migration %s -> %s", m.OldTag, targetTag)
	}
}

// annotateUploadError turns the two migration mistakes people actually hit
// into instructions rather than a bare API code.
func annotateUploadError(err error, cfg *Config, state *State, targetTag string) error {
	var he *HTTPError
	if !asHTTPError(err, &he) {
		return err
	}
	msg := strings.ToLower(he.Error())
	switch {
	case strings.Contains(msg, "already exists"), strings.Contains(msg, "cannot create binding"):
		return fmt.Errorf("%w\n\nhint: class %s already exists on this script. cfdo tracks applied migrations in %s;\n      if that file was lost, set applied_migration_tag there to the tag Cloudflare has,\n      or run `cfdo status` to see the deployed migration tag.",
			err, cfg.ClassName, cfg.statePath())
	case strings.Contains(msg, "migration tag"), strings.Contains(msg, "old_tag"):
		return fmt.Errorf("%w\n\nhint: Cloudflare's migration tag does not match the local one (%q -> %q).\n      Run `cfdo status` to read the deployed tag, then set applied_migration_tag in %s.",
			err, state.AppliedMigrationTag, targetTag, cfg.statePath())
	}
	return err
}

func shorten(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// redactSecrets copies the metadata with secret binding values masked, so a
// dry run can be pasted anywhere without leaking CFDO_SECRET.
func redactSecrets(metadata map[string]any) map[string]any {
	out := maps.Clone(metadata)
	bindings, ok := out["bindings"].([]map[string]any)
	if !ok {
		return out
	}
	masked := make([]map[string]any, len(bindings))
	for i, b := range bindings {
		cp := maps.Clone(b)
		if cp["type"] == "secret_text" {
			cp["text"] = "***redacted***"
		}
		masked[i] = cp
	}
	out["bindings"] = masked
	return out
}
