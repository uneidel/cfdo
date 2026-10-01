package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

func newInitCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Record your Cloudflare credentials in ~/.cfdo/settings.json",
		Long: `Records your Cloudflare credentials in ~/.cfdo/settings.json (mode 0600) so the
commands work without exporting environment variables every session.

Environment variables always win over this file:
  CLOUDFLARE_API_TOKEN   > cloudflare_api_token
  CLOUDFLARE_ACCOUNT_ID  > account_id in cfdo.json > cloudflare_account_id
  CFDO_SECRET            > scripts.<name>.cfdo_secret > cfdo_secret`,
		Args: cobra.NoArgs,
	}
	fs := cmd.Flags()
	token := fs.String("token", "", "Cloudflare API token (prompted for if omitted)")
	account := fs.String("account", "", "Cloudflare account id (prompted for if omitted)")
	secret := fs.String("secret", "", "admin secret for the worker's /__cfdo/ routes")
	script := fs.String("script", "", "record the secret for this script only, instead of as the default")
	genSecret := fs.Bool("generate-secret", false, "generate a random admin secret instead of prompting")
	show := fs.Bool("show", false, "print the current settings and where each value resolves from")
	noVerify := fs.Bool("no-verify", false, "skip checking the token against the Cloudflare API")
	force := fs.Bool("force", false, "overwrite values that are already recorded")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()

		settings, err := loadSettings()
		if err != nil {
			return err
		}

		if *show {
			return showSettings(settings, *script)
		}

		in := bufio.NewReader(os.Stdin)
		interactive := isTerminal(os.Stdin)

		// --- token
		newToken := strings.TrimSpace(*token)
		if newToken == "" && (settings.APIToken == "" || *force) {
			if !interactive {
				return fmt.Errorf("no --token given and stdin is not a terminal")
			}
			newToken, err = promptSecret(in, "Cloudflare API token (Workers Scripts:Edit, Account Settings:Read): ")
			if err != nil {
				return err
			}
		}
		if newToken != "" {
			settings.APIToken = newToken
		}
		if settings.APIToken == "" {
			return fmt.Errorf("an API token is required")
		}

		// --- account
		newAccount := strings.TrimSpace(*account)
		if newAccount == "" && (settings.AccountID == "" || *force) {
			if !interactive {
				return fmt.Errorf("no --account given and stdin is not a terminal")
			}
			newAccount, err = prompt(in, "Cloudflare account id: ")
			if err != nil {
				return err
			}
		}
		if newAccount != "" {
			settings.AccountID = newAccount
		}
		if settings.AccountID == "" {
			return fmt.Errorf("an account id is required")
		}

		// --- admin secret
		newSecret := strings.TrimSpace(*secret)
		if newSecret == "" && *genSecret {
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err != nil {
				return err
			}
			newSecret = base64.RawURLEncoding.EncodeToString(buf)
			fmt.Printf("Generated admin secret: %s\n", newSecret)
		}
		existingSecret := settings.Secret
		if *script != "" {
			existingSecret = settings.Scripts[*script].Secret
		}
		if newSecret == "" && (existingSecret == "" || *force) && interactive {
			newSecret, err = promptSecret(in, "Admin secret (blank to skip, it must match what `cfdo upload` binds): ")
			if err != nil {
				return err
			}
		}
		if newSecret != "" {
			if *script != "" {
				settings.setScriptSecret(*script, newSecret)
			} else {
				settings.Secret = newSecret
			}
		}

		if !*noVerify {
			fmt.Print("Verifying token… ")
			if err := verifyToken(ctx, settings.APIToken, settings.AccountID); err != nil {
				fmt.Println("failed")
				return fmt.Errorf("%w\n  Nothing was written. Re-run with --no-verify to save anyway.", err)
			}
			fmt.Println("ok")
		}

		if err := settings.save(); err != nil {
			return err
		}
		fmt.Printf("Wrote %s (mode 0600)\n", settings.path)
		return showSettings(settings, *script)
	}
	return cmd
}

// verifyToken checks the token can actually reach the account. The
// account-scoped verify endpoint is the one that works for account tokens;
// /user/tokens/verify rejects them.
func verifyToken(ctx context.Context, token, accountID string) error {
	c := NewClient(token)
	if _, err := c.ListNamespaces(ctx, accountID); err != nil {
		var he *HTTPError
		if asHTTPError(err, &he) && (he.Status == 401 || he.Status == 403) {
			return fmt.Errorf("token cannot read account %s: %w\n  It needs Workers Scripts:Edit and Account Settings:Read.", accountID, err)
		}
		return err
	}
	return nil
}

func showSettings(s *Settings, script string) error {
	fmt.Printf("\n%s\n", s.path)
	fmt.Printf("  %-22s %s\n", "cloudflare_api_token", mask(s.APIToken))
	fmt.Printf("  %-22s %s\n", "cloudflare_account_id", orNotSet(s.AccountID))
	fmt.Printf("  %-22s %s\n", "cfdo_secret", mask(s.Secret))
	for name, entry := range s.Scripts {
		fmt.Printf("  %-22s %s\n", "scripts."+name, mask(entry.Secret))
	}

	fmt.Println("\nResolved now (environment wins):")
	tok, tokErr := resolveAPIToken(s)
	fmt.Printf("  %-22s %s %s\n", "API token", maskOrErr(tok, tokErr), sourceOf("CLOUDFLARE_API_TOKEN", s.APIToken != ""))
	acct := resolveAccountID(s, "")
	fmt.Printf("  %-22s %s %s\n", "account id", orNotSet(acct), sourceOf("CLOUDFLARE_ACCOUNT_ID", s.AccountID != ""))
	if script != "" {
		sec, secErr := resolveSecret(s, script)
		fmt.Printf("  %-22s %s %s\n", "secret for "+script, maskOrErr(sec, secErr), sourceOf("CFDO_SECRET", true))
	}
	return nil
}

func sourceOf(env string, inFile bool) string {
	switch {
	case os.Getenv(env) != "":
		return "(from " + env + ")"
	case inFile:
		return "(from settings.json)"
	default:
		return ""
	}
}

func maskOrErr(v string, err error) string {
	if err != nil {
		return "(not set)"
	}
	return mask(v)
}

func orNotSet(v string) string {
	if v == "" {
		return "(not set)"
	}
	return v
}

func prompt(in *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// promptSecret turns off terminal echo so a pasted token does not end up on
// screen or in a scrollback buffer.
func promptSecret(in *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	restore, ok := disableEcho()
	line, err := in.ReadString('\n')
	if ok {
		restore()
		fmt.Println()
	}
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func disableEcho() (func(), bool) {
	if !isTerminal(os.Stdin) {
		return nil, false
	}
	saved, err := exec.Command("stty", "-f", "/dev/tty", "-g").Output()
	if err != nil {
		return nil, false
	}
	if err := exec.Command("stty", "-f", "/dev/tty", "-echo").Run(); err != nil {
		return nil, false
	}
	return func() {
		exec.Command("stty", "-f", "/dev/tty", strings.TrimSpace(string(saved))).Run()
	}, true
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// `go test` and many CI runners attach /dev/null to stdin. It is a
	// character device but not a terminal, and prompting against it blocks.
	if devNull, err := os.Stat(os.DevNull); err == nil && os.SameFile(info, devNull) {
		return false
	}
	return true
}
