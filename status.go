package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	confPath := fs.String("c", "", "path to cfdo.json (default: nearest one up the tree)")
	showObjects := fs.Bool("objects", false, "list every object id")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	noPing := fs.Bool("no-ping", false, "skip the live check against the worker's admin route")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: cfdo status [flags]\n\nShows the deployed script, its Durable Object namespace and object count.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(permute(fs, args)); err != nil {
		return err
	}

	cfg, err := loadConfig(*confPath)
	if err != nil {
		return err
	}
	state, err := cfg.loadState()
	if err != nil {
		return err
	}
	token, err := cfg.token()
	if err != nil {
		return err
	}
	client := NewClient(token)

	report := map[string]any{
		"script":                cfg.ScriptName,
		"class":                 cfg.ClassName,
		"binding":               cfg.Binding,
		"sqlite":                cfg.SQLite,
		"local_migration_tag":   cfg.MigrationTag,
		"applied_migration_tag": state.AppliedMigrationTag,
		"last_uploaded_at":      state.LastUploadedAt,
	}

	script, err := client.GetScript(ctx, cfg.AccountID, cfg.ScriptName)
	if err != nil {
		return err
	}
	deployed := script != nil
	report["deployed"] = deployed
	if deployed {
		report["modified_on"] = script.ModifiedOn
		if settings, err := client.GetScriptSettings(ctx, cfg.AccountID, cfg.ScriptName); err == nil {
			report["compatibility_date"] = settings.CompatibilityDate
			if settings.MigrationTag != "" {
				report["remote_migration_tag"] = settings.MigrationTag
			}
			report["bindings"] = bindingNames(settings.Bindings)
		}
	}

	var ns *Namespace
	var objects []DOObject
	if deployed {
		ns, err = client.FindNamespace(ctx, cfg.AccountID, cfg.ScriptName, cfg.ClassName)
		if err != nil {
			report["namespace_error"] = err.Error()
		} else {
			report["namespace_id"] = ns.ID
			report["namespace_sqlite"] = ns.UseSQLite
			objects, err = client.ListObjects(ctx, cfg.AccountID, ns.ID)
			if err != nil {
				report["objects_error"] = err.Error()
			} else {
				report["object_count"] = len(objects)
				report["objects_with_data"] = countWithData(objects)
				if *showObjects {
					ids := make([]string, 0, len(objects))
					for _, o := range objects {
						ids = append(ids, o.ID)
					}
					report["object_ids"] = ids
				}
			}
			if state.NamespaceID != ns.ID {
				state.NamespaceID = ns.ID
				_ = cfg.saveState(state)
			}
		}
	}

	sub, _ := client.WorkersDevSubdomain(ctx, cfg.AccountID)
	workerURL, urlErr := cfg.resolveWorkerURL(sub)
	if urlErr == nil {
		report["worker_url"] = workerURL
	}

	var indexed []indexEntry
	if !*noPing && deployed && urlErr == nil {
		if secret, err := cfg.secret(); err != nil {
			report["admin"] = "skipped: no admin secret (run `cfdo init`)"
		} else {
			admin := newAdminClient(workerURL, secret)
			if p, err := admin.Ping(ctx); err != nil {
				report["admin"] = "unreachable: " + err.Error()
			} else {
				report["admin"] = fmt.Sprintf("ok (format %d)", p.Format)
				if indexed, err = admin.List(ctx); err == nil {
					report["indexed_objects"] = len(indexed)
				}
			}
		}
	}

	if len(indexed) > 0 {
		report["index"] = indexed
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	row := func(k string, v any) {
		if v == nil || v == "" {
			return
		}
		fmt.Fprintf(tw, "%s\t%v\n", k, v)
	}
	row("script", cfg.ScriptName)
	row("account", cfg.AccountID)
	if deployed {
		row("deployed", "yes, modified "+script.ModifiedOn)
	} else {
		row("deployed", "no — run `cfdo upload`")
	}
	row("compatibility date", report["compatibility_date"])
	row("class", fmt.Sprintf("%s (%s)", cfg.ClassName, storageKind(cfg.SQLite)))
	row("binding", cfg.Binding)
	row("bindings live", joinAny(report["bindings"]))
	row("migration tag", migrationLine(cfg, state, report))
	row("namespace", report["namespace_id"])
	row("namespace error", report["namespace_error"])
	if _, ok := report["object_count"]; ok {
		line := fmt.Sprintf("%d via API", report["object_count"])
		if ns != nil && ns.UseSQLite {
			// For SQLite-backed namespaces the listing API is lagging and
			// incomplete, so its count is a floor, not a total.
			line += " (incomplete: the API lags for SQLite-backed namespaces)"
		}
		if n, ok := report["indexed_objects"]; ok {
			line += fmt.Sprintf("; %v in the worker index", n)
		}
		row("objects", line)
	}
	row("objects error", report["objects_error"])
	row("worker url", report["worker_url"])
	row("admin route", report["admin"])
	row("last upload", state.LastUploadedAt)
	tw.Flush()

	if *showObjects {
		fmt.Println()
		for _, o := range objects {
			mark := " "
			if o.HasStoredData {
				mark = "*"
			}
			fmt.Printf("%s %s\n", mark, o.ID)
		}
		for _, e := range indexed {
			fmt.Printf("  %s  %s\n", e.ID, e.Name)
		}
		if len(objects) == 0 && len(indexed) == 0 {
			fmt.Println("  (no objects discovered)")
		}
	}
	return nil
}

func migrationLine(cfg *Config, state *State, report map[string]any) string {
	s := fmt.Sprintf("local %s", cfg.MigrationTag)
	if state.AppliedMigrationTag == "" {
		s += ", never applied"
	} else if state.AppliedMigrationTag != cfg.MigrationTag {
		s += fmt.Sprintf(", applied %s — upload will migrate", state.AppliedMigrationTag)
	} else {
		s += ", applied"
	}
	if remote, ok := report["remote_migration_tag"]; ok && remote != state.AppliedMigrationTag {
		s += fmt.Sprintf(" (Cloudflare reports %v)", remote)
	}
	return s
}

func bindingNames(bs []map[string]any) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		name, _ := b["name"].(string)
		typ, _ := b["type"].(string)
		out = append(out, fmt.Sprintf("%s:%s", name, typ))
	}
	return out
}

func countWithData(objs []DOObject) int {
	n := 0
	for _, o := range objs {
		if o.HasStoredData {
			n++
		}
	}
	return n
}

func joinAny(v any) string {
	ss, ok := v.([]string)
	if !ok || len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += ", " + s
	}
	return out
}
