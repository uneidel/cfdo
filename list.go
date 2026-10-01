package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// namespaceReport is one row of `cfdo list`.
type namespaceReport struct {
	Script       string   `json:"script"`
	Class        string   `json:"class"`
	NamespaceID  string   `json:"namespace_id"`
	SQLite       bool     `json:"sqlite"`
	Deployed     bool     `json:"deployed"`
	ModifiedOn   string   `json:"modified_on,omitempty"`
	APIObjects   int      `json:"api_objects"`
	APIError     string   `json:"api_error,omitempty"`
	IndexObjects *int     `json:"index_objects,omitempty"`
	IndexError   string   `json:"index_error,omitempty"`
	ObjectIDs    []string `json:"object_ids,omitempty"`
}

func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every Durable Object namespace on the account",
		Long: `Lists every Durable Object namespace on the account. Needs no cfdo.json, so it
works from anywhere — use it to see what exists before picking a project.`,
		Args: cobra.NoArgs,
	}
	fs := cmd.Flags()
	account := fs.String("account", "", "Cloudflare account id (default: environment, else ~/.cfdo/settings.json)")
	scriptFilter := fs.String("script", "", "only namespaces belonging to this script")
	showObjects := fs.Bool("objects", false, "list object ids under each namespace")
	deep := fs.Bool("deep", false, "also ask each worker's index for its object count (needs a secret per script)")
	asJSON := fs.Bool("json", false, "emit machine-readable JSON")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		settings, err := loadSettings()
		if err != nil {
			return err
		}
		accountID := strings.TrimSpace(*account)
		if accountID == "" {
			accountID = resolveAccountID(settings, "")
		}
		if accountID == "" {
			return fmt.Errorf("no account id — pass --account, set CLOUDFLARE_ACCOUNT_ID, or run `cfdo init`")
		}
		token, err := resolveAPIToken(settings)
		if err != nil {
			return err
		}
		client := NewClient(token)

		namespaces, err := client.ListNamespaces(ctx, accountID)
		if err != nil {
			return fmt.Errorf("listing namespaces: %w", err)
		}
		scripts, err := client.ListScripts(ctx, accountID)
		if err != nil {
			// A token without script read access can still list namespaces.
			fmt.Fprintf(os.Stderr, "warning: could not list scripts: %v\n", err)
		}
		deployed := map[string]ScriptInfo{}
		for _, s := range scripts {
			deployed[s.ID] = s
		}

		var subdomain string
		if *deep {
			subdomain, _ = client.WorkersDevSubdomain(ctx, accountID)
		}

		rows := make([]namespaceReport, 0, len(namespaces))
		for _, ns := range namespaces {
			if *scriptFilter != "" && ns.Script != *scriptFilter {
				continue
			}
			row := namespaceReport{
				Script: ns.Script, Class: ns.Class,
				NamespaceID: ns.ID, SQLite: ns.UseSQLite,
			}
			if info, ok := deployed[ns.Script]; ok {
				row.Deployed = true
				row.ModifiedOn = info.ModifiedOn
			}

			objects, err := client.ListObjects(ctx, accountID, ns.ID)
			if err != nil {
				row.APIError = err.Error()
			} else {
				row.APIObjects = len(objects)
				if *showObjects {
					for _, o := range objects {
						row.ObjectIDs = append(row.ObjectIDs, o.ID)
					}
				}
			}

			if *deep {
				n, err := indexCount(ctx, settings, ns.Script, subdomain, &row)
				if err != nil {
					row.IndexError = err.Error()
				} else {
					row.IndexObjects = &n
				}
			}
			rows = append(rows, row)
		}

		slices.SortFunc(rows, func(a, b namespaceReport) int {
			if c := strings.Compare(a.Script, b.Script); c != 0 {
				return c
			}
			return strings.Compare(a.Class, b.Class)
		})

		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"account_id": accountID, "namespaces": rows})
		}

		if len(rows) == 0 {
			if *scriptFilter != "" {
				fmt.Printf("No Durable Object namespaces for script %q on account %s.\n", *scriptFilter, accountID)
			} else {
				fmt.Printf("No Durable Object namespaces on account %s.\n", accountID)
			}
			return nil
		}

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		header := "SCRIPT\tCLASS\tSTORAGE\tOBJECTS\tNAMESPACE"
		if *deep {
			header = "SCRIPT\tCLASS\tSTORAGE\tOBJECTS\tINDEX\tNAMESPACE"
		}
		fmt.Fprintln(tw, header)
		anySQLite := false
		for _, r := range rows {
			storage := "kv"
			if r.SQLite {
				storage = "sqlite"
				anySQLite = true
			}
			objects := fmt.Sprint(r.APIObjects)
			if r.APIError != "" {
				objects = "?"
			}
			script := r.Script
			if !r.Deployed {
				script += " (not deployed)"
			}
			if *deep {
				index := "-"
				switch {
				case r.IndexObjects != nil:
					index = fmt.Sprint(*r.IndexObjects)
				case r.IndexError != "":
					index = "?"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", script, r.Class, storage, objects, index, r.NamespaceID)
			} else {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", script, r.Class, storage, objects, r.NamespaceID)
			}
		}
		tw.Flush()

		if anySQLite {
			fmt.Println("\nOBJECTS is Cloudflare's listing API. For sqlite namespaces it lags badly — it can")
			fmt.Println("report 0 for a namespace with live objects — so treat a low number as unreliable")
			fmt.Println("rather than as a total. `cfdo list --deep` adds INDEX, the count from each worker's")
			fmt.Println("own registry.")
		}
		if *deep && anySQLite {
			fmt.Println("\nThe two columns measure different things and disagree in both directions: OBJECTS")
			fmt.Println("counts the __cfdo_index bookkeeping object, which INDEX omits, while INDEX only")
			fmt.Println("knows objects routed by name through the generated code. `cfdo backup` unions both.")
		}
		if *deep {
			for _, r := range rows {
				if r.IndexError != "" {
					fmt.Fprintf(os.Stderr, "\n%s: index unavailable — %s\n", r.Script, r.IndexError)
				}
			}
		}

		if *showObjects {
			for _, r := range rows {
				fmt.Printf("\n%s/%s (%d)\n", r.Script, r.Class, len(r.ObjectIDs))
				for _, id := range r.ObjectIDs {
					fmt.Printf("  %s\n", id)
				}
			}
		}
		return nil
	}
	return cmd
}

// indexCount asks the worker's own index how many objects it knows about. It
// needs the script's admin secret and a reachable workers.dev hostname, so it
// is best-effort and only runs under --deep.
func indexCount(ctx context.Context, settings *Settings, script, subdomain string, row *namespaceReport) (int, error) {
	secret, err := resolveSecret(settings, script)
	if err != nil {
		return 0, fmt.Errorf("no secret recorded for %s", script)
	}
	if subdomain == "" {
		return 0, fmt.Errorf("account has no workers.dev subdomain; cannot guess the worker URL")
	}
	admin := newAdminClient(fmt.Sprintf("https://%s.%s.workers.dev", script, subdomain), secret)
	entries, err := admin.List(ctx)
	if err != nil {
		return 0, err
	}
	if len(row.ObjectIDs) == 0 {
		for _, e := range entries {
			row.ObjectIDs = append(row.ObjectIDs, e.ID)
		}
	}
	return len(entries), nil
}
