package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete [script] [--ns <namespace>]",
		Short: "Delete a worker and all of its Durable Object data",
		Long: `Deletes a worker script together with every Durable Object namespace it defines
and all of their stored data. Pick the script by name, or with --ns by one of its
namespaces (id or name) — a script's namespaces are deleted together, so --ns
removes the whole script too.

This cannot be undone. Run ` + "`cfdo backup`" + ` first.`,
		Args: cobra.MaximumNArgs(1),
	}
	fs := cmd.Flags()
	ns := fs.String("ns", "", "delete the script that owns this namespace (id or name)")
	account := fs.String("account", "", "Cloudflare account id (default: environment, else ~/.cfdo/settings.json)")
	yes := fs.BoolP("yes", "y", false, "skip the confirmation prompt")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		script := ""
		if len(args) == 1 {
			script = args[0]
		}

		client, settings, accountID, err := accountClient(*account)
		if err != nil {
			return err
		}
		t, err := resolveTarget(ctx, client, settings, accountID, script, *ns)
		if err != nil {
			return err
		}
		info, err := client.GetScript(ctx, accountID, t.Script)
		if err != nil {
			return err
		}
		if info == nil {
			if len(t.Namespaces) > 0 {
				return fmt.Errorf("script %q is not deployed but still owns %d namespace(s); Cloudflare only removes those through the script", t.Script, len(t.Namespaces))
			}
			return fmt.Errorf("no script %q on account %s", t.Script, accountID)
		}

		fmt.Printf("This permanently deletes worker %q on account %s", t.Script, accountID)
		if len(t.Namespaces) == 0 {
			fmt.Println(".")
		} else {
			fmt.Println(" and all data in:")
			for _, n := range t.Namespaces {
				count := "? objects"
				if objs, err := client.ListObjects(ctx, accountID, n.ID); err == nil {
					count = fmt.Sprintf("%d objects", len(objs))
					if n.UseSQLite {
						count += " or more"
					}
				}
				fmt.Printf("  %s  %s (%s, %s)\n", n.ID, n.Class, storageKind(n.UseSQLite), count)
			}
		}

		if !*yes {
			if !isTerminal(os.Stdin) {
				return fmt.Errorf("refusing to delete without confirmation; pass --yes")
			}
			fmt.Printf("Type the script name to confirm: ")
			var answer string
			fmt.Scanln(&answer)
			if strings.TrimSpace(answer) != t.Script {
				return fmt.Errorf("aborted")
			}
		}

		if err := client.DeleteScript(ctx, accountID, t.Script, true); err != nil {
			return fmt.Errorf("deleting %s: %w", t.Script, err)
		}
		fmt.Printf("Deleted %s", t.Script)
		if len(t.Namespaces) > 0 {
			fmt.Printf(" and %d namespace(s)", len(t.Namespaces))
		}
		fmt.Println(".")

		forgetUpload(accountID, t.Script)
		return nil
	}
	return cmd
}

// forgetUpload clears the applied migration tag of a matching project in
// the working tree, so the next upload sends the full class list again
// instead of an old_tag the deleted script no longer has.
func forgetUpload(accountID, script string) {
	path, err := findUp(configName)
	if err != nil {
		return
	}
	cfg, err := loadConfig(path)
	if err != nil || cfg.ScriptName != script || cfg.AccountID != accountID {
		return
	}
	if err := cfg.saveState(&State{}); err == nil {
		fmt.Printf("Reset %s; the next upload starts from a fresh migration.\n",
			filepath.Join(stateDir, stateName))
	}
}
