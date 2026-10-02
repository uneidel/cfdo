package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

type remoteNamespace struct {
	ID         string   `json:"namespace_id"`
	Class      string   `json:"class"`
	SQLite     bool     `json:"sqlite"`
	Objects    *int     `json:"object_count,omitempty"`
	WithData   *int     `json:"objects_with_data,omitempty"`
	ObjectsErr string   `json:"objects_error,omitempty"`
	ObjectIDs  []string `json:"object_ids,omitempty"`
}

// remoteStatus is `cfdo status <script>` / `--ns`: what Cloudflare and the
// worker itself report, with no local project to compare against.
func remoteStatus(ctx context.Context, account, script, ns string, showObjects, asJSON, noPing bool) error {
	client, settings, accountID, err := accountClient(account)
	if err != nil {
		return err
	}
	t, err := resolveTarget(ctx, client, settings, accountID, script, ns)
	if err != nil {
		return err
	}
	info, err := client.GetScript(ctx, accountID, t.Script)
	if err != nil {
		return err
	}
	if info == nil && len(t.Namespaces) == 0 {
		return fmt.Errorf("no script %q on account %s (see `cfdo list`)", t.Script, accountID)
	}

	report := map[string]any{
		"script":     t.Script,
		"account_id": accountID,
		"deployed":   info != nil,
	}
	if info != nil {
		report["modified_on"] = info.ModifiedOn
		if s, err := client.GetScriptSettings(ctx, accountID, t.Script); err == nil {
			report["compatibility_date"] = s.CompatibilityDate
			if s.MigrationTag != "" {
				report["remote_migration_tag"] = s.MigrationTag
			}
			report["bindings"] = bindingNames(s.Bindings)
		}
	}

	nss := make([]remoteNamespace, 0, len(t.Namespaces))
	for _, n := range t.Namespaces {
		r := remoteNamespace{ID: n.ID, Class: n.Class, SQLite: n.UseSQLite}
		objs, err := client.ListObjects(ctx, accountID, n.ID)
		if err != nil {
			r.ObjectsErr = err.Error()
		} else {
			count, withData := len(objs), countWithData(objs)
			r.Objects, r.WithData = &count, &withData
			if showObjects {
				for _, o := range objs {
					r.ObjectIDs = append(r.ObjectIDs, o.ID)
				}
			}
		}
		nss = append(nss, r)
	}
	report["namespaces"] = nss

	// Only workers.dev can be guessed; a custom route lives in cfdo.json.
	var indexed []indexEntry
	if sub, _ := client.WorkersDevSubdomain(ctx, accountID); sub != "" && info != nil {
		workerURL := fmt.Sprintf("https://%s.%s.workers.dev", t.Script, sub)
		report["worker_url"] = workerURL
		if !noPing {
			if secret, err := resolveSecret(settings, t.Script); err != nil {
				report["admin"] = "skipped: no admin secret recorded for this script"
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
	}
	if len(indexed) > 0 {
		report["index"] = indexed
	}

	if asJSON {
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
	row("script", t.Script)
	row("account", accountID)
	if info != nil {
		row("deployed", "yes, modified "+info.ModifiedOn)
	} else {
		row("deployed", "no — the namespaces below are orphaned")
	}
	row("compatibility date", report["compatibility_date"])
	row("bindings live", joinAny(report["bindings"]))
	row("migration tag", report["remote_migration_tag"])
	for _, n := range nss {
		line := fmt.Sprintf("%s (%s) %s", n.Class, storageKind(n.SQLite), n.ID)
		switch {
		case n.ObjectsErr != "":
			line += " — objects: " + n.ObjectsErr
		case n.SQLite:
			line += fmt.Sprintf(" — %d objects via API (incomplete for SQLite)", *n.Objects)
		default:
			line += fmt.Sprintf(" — %d objects via API", *n.Objects)
		}
		row("namespace", line)
	}
	if len(nss) == 0 {
		row("namespace", "none — this script defines no Durable Objects")
	}
	row("worker index", report["indexed_objects"])
	row("worker url", report["worker_url"])
	row("admin route", report["admin"])
	tw.Flush()

	if showObjects {
		for _, n := range nss {
			fmt.Printf("\n%s (%d)\n", n.Class, len(n.ObjectIDs))
			for _, id := range n.ObjectIDs {
				fmt.Printf("  %s\n", id)
			}
		}
		if len(indexed) > 0 {
			fmt.Printf("\nworker index (%d)\n", len(indexed))
			for _, e := range indexed {
				fmt.Printf("  %s  %s\n", e.ID, e.Name)
			}
		}
	}
	return nil
}
