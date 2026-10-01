package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	settingsDirName    = ".cfdo"
	settingsFileName   = "settings.json"
	accountPlaceholder = "REPLACE_WITH_ACCOUNT_ID"
)

// ScriptEntry holds values that differ per worker. Secrets do: two projects
// on the same account have two different CFDO_SECRETs.
type ScriptEntry struct {
	Secret string `json:"cfdo_secret,omitempty"`
}

// Settings is the user-wide config at ~/.cfdo/settings.json. It is a fallback
// for the environment variables, never an override of them.
type Settings struct {
	APIToken  string                 `json:"cloudflare_api_token,omitempty"`
	AccountID string                 `json:"cloudflare_account_id,omitempty"`
	Secret    string                 `json:"cfdo_secret,omitempty"`
	Scripts   map[string]ScriptEntry `json:"scripts,omitempty"`

	path string
}

func settingsPath() (string, error) {
	base := os.Getenv("CFDO_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locating home directory: %w", err)
		}
		base = home
	}
	return filepath.Join(base, settingsDirName, settingsFileName), nil
}

// loadSettings reads ~/.cfdo/settings.json. A missing file is not an error —
// everything can still come from the environment.
func loadSettings() (*Settings, error) {
	path, err := settingsPath()
	if err != nil {
		return nil, err
	}
	s := &Settings{path: path}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	s.path = path
	return s, nil
}

func (s *Settings) save() error {
	if s.path == "" {
		p, err := settingsPath()
		if err != nil {
			return err
		}
		s.path = p
	}
	// The file holds an API token, so keep both it and its directory private.
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.path, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(s.path, 0o600)
}

func (s *Settings) setScriptSecret(script, secret string) {
	if s.Scripts == nil {
		s.Scripts = map[string]ScriptEntry{}
	}
	entry := s.Scripts[script]
	entry.Secret = secret
	s.Scripts[script] = entry
}

// ---------------------------------------------------------------- resolution
//
// Precedence is the same everywhere: environment first, then the project's
// cfdo.json where it applies, then these user-wide settings.

func resolveAPIToken(s *Settings) (string, error) {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_API_TOKEN")); v != "" {
		return v, nil
	}
	if s != nil && s.APIToken != "" {
		return s.APIToken, nil
	}
	return "", fmt.Errorf("no Cloudflare API token — set CLOUDFLARE_API_TOKEN or run `cfdo init`")
}

// resolveSecret prefers a secret recorded for this specific script, because a
// single global secret is wrong as soon as there are two projects.
func resolveSecret(s *Settings, script string) (string, error) {
	if v := strings.TrimSpace(os.Getenv("CFDO_SECRET")); v != "" {
		return v, nil
	}
	if s != nil {
		if entry, ok := s.Scripts[script]; ok && entry.Secret != "" {
			return entry.Secret, nil
		}
		if s.Secret != "" {
			return s.Secret, nil
		}
	}
	return "", fmt.Errorf("no admin secret for %q — set CFDO_SECRET or run `cfdo init -script %s`", script, script)
}

func resolveAccountID(s *Settings, fromConfig string) string {
	if v := strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID")); v != "" {
		return v
	}
	if fromConfig != "" && fromConfig != accountPlaceholder {
		return fromConfig
	}
	if s != nil {
		return s.AccountID
	}
	return ""
}

// mask renders a credential for display without disclosing it.
func mask(v string) string {
	if v == "" {
		return "(not set)"
	}
	if len(v) <= 8 {
		return strings.Repeat("•", len(v))
	}
	return v[:4] + strings.Repeat("•", 8) + v[len(v)-4:]
}
