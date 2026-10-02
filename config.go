package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	configName = "cfdo.json"
	stateDir   = ".cfdo"
	stateName  = "state.json"
)

// Config is the project file (cfdo.json) written by `cfdo create`.
type Config struct {
	AccountID         string       `json:"account_id"`
	ScriptName        string       `json:"script_name"`
	ClassName         string       `json:"class_name"`
	Binding           string       `json:"binding"`
	MainModule        string       `json:"main_module"`
	CompatibilityDate string       `json:"compatibility_date"`
	SQLite            bool         `json:"sqlite"`
	MigrationTag      string       `json:"migration_tag"`
	WorkerURL         string       `json:"worker_url,omitempty"`
	WorkersDev        bool         `json:"workers_dev"`
	Assets            string       `json:"assets,omitempty"` // directory served as static files, e.g. "public"
	Plugins           []PluginLock `json:"plugins,omitempty"`

	dir      string    // directory the config was loaded from; not serialised
	path     string    // the config file itself
	settings *Settings // user-wide fallbacks, loaded alongside
}

// State tracks what we have actually pushed, so upload can compute the
// old_tag -> new_tag migration pair the Durable Objects API expects.
type State struct {
	AppliedMigrationTag string `json:"applied_migration_tag"`
	LastUploadedAt      string `json:"last_uploaded_at,omitempty"`
	NamespaceID         string `json:"namespace_id,omitempty"`
}

// loadConfig finds cfdo.json at path (or walks up from the working
// directory when path is empty) and applies environment overrides.
func loadConfig(path string) (*Config, error) {
	if path == "" {
		found, err := findUp(configName)
		if err != nil {
			return nil, err
		}
		path = found
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.dir = filepath.Dir(abs)
	c.path = abs

	settings, err := loadSettings()
	if err != nil {
		return nil, err
	}
	c.AccountID = resolveAccountID(settings, c.AccountID)
	c.settings = settings

	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) validate() error {
	missing := []string{}
	for name, v := range map[string]string{
		"account_id":    c.AccountID,
		"script_name":   c.ScriptName,
		"class_name":    c.ClassName,
		"binding":       c.Binding,
		"main_module":   c.MainModule,
		"migration_tag": c.MigrationTag,
	} {
		if strings.TrimSpace(v) == "" || v == accountPlaceholder {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(sorted(missing), ", "))
	}
	return nil
}

func (c *Config) save(path string) error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (c *Config) statePath() string { return filepath.Join(c.dir, stateDir, stateName) }

func (c *Config) loadState() (*State, error) {
	b, err := os.ReadFile(c.statePath())
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", c.statePath(), err)
	}
	return &s, nil
}

func (c *Config) saveState(s *State) error {
	if err := os.MkdirAll(filepath.Join(c.dir, stateDir), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.statePath(), append(b, '\n'), 0o644)
}

// resolveWorkerURL returns the base URL of the deployed worker, preferring
// an explicit worker_url over the account's workers.dev subdomain.
func (c *Config) resolveWorkerURL(subdomain string) (string, error) {
	if c.WorkerURL != "" {
		return strings.TrimRight(c.WorkerURL, "/"), nil
	}
	if subdomain == "" {
		return "", fmt.Errorf("no worker_url in %s and the account has no workers.dev subdomain; set worker_url to the route the worker is served on", configName)
	}
	return fmt.Sprintf("https://%s.%s.workers.dev", c.ScriptName, subdomain), nil
}

func findUp(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s found in this directory or any parent (run `cfdo create` first, or pass -c)", name)
		}
		dir = parent
	}
}

// token and secret resolve through the user-wide settings loaded with the
// project config, so commands do not each have to reload them.
func (c *Config) token() (string, error)  { return resolveAPIToken(c.settings) }
func (c *Config) secret() (string, error) { return resolveSecret(c.settings, c.ScriptName) }

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
