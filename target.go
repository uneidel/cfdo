package main

import (
	"context"
	"fmt"
	"strings"
)

// target is a script picked on the command line, either by name or through
// one of its namespaces, for commands that work without a cfdo.json.
type target struct {
	AccountID  string
	Script     string
	Namespaces []Namespace // every namespace the script defines
	Settings   *Settings
}

// resolveTarget accepts exactly one of script or ns. ns matches a namespace
// id or its name; either way the result covers the whole owning script.
func resolveTarget(ctx context.Context, client *Client, settings *Settings, accountID, script, ns string) (*target, error) {
	script, ns = strings.TrimSpace(script), strings.TrimSpace(ns)
	switch {
	case script == "" && ns == "":
		return nil, fmt.Errorf("name a script or pass --ns <namespace>")
	case script != "" && ns != "":
		return nil, fmt.Errorf("pass a script name or --ns, not both")
	}

	namespaces, err := client.ListNamespaces(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("listing namespaces: %w", err)
	}
	if ns != "" {
		for _, n := range namespaces {
			if n.ID == ns || n.Name == ns {
				script = n.Script
				break
			}
		}
		if script == "" {
			return nil, fmt.Errorf("no Durable Object namespace %q on account %s (see `cfdo list`)", ns, accountID)
		}
	}

	t := &target{AccountID: accountID, Script: script, Settings: settings}
	for _, n := range namespaces {
		if n.Script == script {
			t.Namespaces = append(t.Namespaces, n)
		}
	}
	return t, nil
}

// accountClient resolves the account and token the way `cfdo list` does,
// so these commands work from any directory.
func accountClient(account string) (*Client, *Settings, string, error) {
	settings, err := loadSettings()
	if err != nil {
		return nil, nil, "", err
	}
	accountID := strings.TrimSpace(account)
	if accountID == "" {
		accountID = resolveAccountID(settings, "")
	}
	if accountID == "" {
		return nil, nil, "", fmt.Errorf("no account id — pass --account, set CLOUDFLARE_ACCOUNT_ID, or run `cfdo init`")
	}
	token, err := resolveAPIToken(settings)
	if err != nil {
		return nil, nil, "", err
	}
	return NewClient(token), settings, accountID, nil
}
