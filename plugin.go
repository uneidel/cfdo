package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const (
	pluginManifestName = "cfdo-plugin.json"
	pluginsDir         = "plugins"
)

var pluginNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// PluginManifest is cfdo-plugin.json at the root of a plugin's source. It
// tells cfdo which files to vendor, so the plugin can reorganise itself
// without cfdo knowing its layout.
type PluginManifest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Worker  string `json:"worker"`          // directory whose files the worker imports
	Entry   string `json:"entry"`           // module inside Worker that user code imports
	Skill   string `json:"skill,omitempty"` // Claude Code skill describing how to use it
}

// PluginLock is what cfdo.json records about a vendored plugin.
type PluginLock struct {
	Name    string `json:"name"`
	Source  string `json:"source"`
	Version string `json:"version,omitempty"`
	SHA256  string `json:"sha256"`
}

func readPluginManifest(source string) (*PluginManifest, error) {
	p := filepath.Join(source, pluginManifestName)
	raw, err := os.ReadFile(p)
	if err != nil {
		return nil, fmt.Errorf("reading plugin manifest: %w", err)
	}
	var m PluginManifest
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	switch {
	case !pluginNameRe.MatchString(m.Name):
		return nil, fmt.Errorf("%s: name %q must be lowercase alphanumeric with dashes", p, m.Name)
	case m.Worker == "" || m.Entry == "":
		return nil, fmt.Errorf("%s: worker and entry are required", p)
	}
	for _, rel := range []string{m.Worker, m.Entry, m.Skill} {
		if filepath.IsAbs(rel) || strings.HasPrefix(filepath.Clean(rel), "..") {
			return nil, fmt.Errorf("%s: %q must be a path inside the plugin", p, rel)
		}
	}
	return &m, nil
}

// vendorPlugin copies a plugin's worker files into <project>/plugins/<name>/
// and its skill into .claude/skills/<name>/, replacing what was there: both
// belong to cfdo, user code only imports them. It returns the lock entry.
func vendorPlugin(project, source string) (*PluginLock, error) {
	source, err := filepath.Abs(source)
	if err != nil {
		return nil, err
	}
	m, err := readPluginManifest(source)
	if err != nil {
		return nil, err
	}
	src := filepath.Join(source, m.Worker)
	if _, err := os.Stat(filepath.Join(src, m.Entry)); err != nil {
		return nil, fmt.Errorf("plugin %s: entry %s not found in %s", m.Name, m.Entry, src)
	}

	dst := filepath.Join(project, pluginsDir, m.Name)
	if err := os.RemoveAll(dst); err != nil {
		return nil, err
	}
	h := sha256.New()
	err = filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		return nil, fmt.Errorf("vendoring plugin %s: %w", m.Name, err)
	}

	if m.Skill != "" {
		skill, err := os.ReadFile(filepath.Join(source, m.Skill))
		if err != nil {
			return nil, fmt.Errorf("plugin %s: %w", m.Name, err)
		}
		h.Write(skill)
		skillPath := filepath.Join(project, ".claude", "skills", m.Name, "SKILL.md")
		if err := os.MkdirAll(filepath.Dir(skillPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(skillPath, []byte(pluginSkill(string(skill), m)), 0o644); err != nil {
			return nil, err
		}
	}

	return &PluginLock{
		Name:    m.Name,
		Source:  source,
		Version: m.Version,
		SHA256:  hex.EncodeToString(h.Sum(nil)),
	}, nil
}

// pluginSkill inserts a note after the skill's frontmatter saying where the
// plugin lives in this project, since its own docs describe its repo layout.
func pluginSkill(skill string, m *PluginManifest) string {
	note := fmt.Sprintf("> **In this cfdo project** the plugin is vendored at `%s/%s/`. Import it from\n"+
		"> the worker as `import { … } from \"./%s/%s/%s\";` — `cfdo upload` uploads every module\n"+
		"> the worker imports, so nothing else is needed. Do not edit files under `%s/`; run\n"+
		"> `cfdo plugin update %s` to pull a new version (this file is replaced too).\n\n",
		pluginsDir, m.Name, pluginsDir, m.Name, m.Entry, pluginsDir, m.Name)
	if rest, ok := strings.CutPrefix(skill, "---\n"); ok {
		if i := strings.Index(rest, "\n---\n"); i >= 0 {
			end := len("---\n") + i + len("\n---\n")
			return skill[:end] + "\n" + note + strings.TrimLeft(skill[end:], "\n")
		}
	}
	return note + skill
}

// addPlugin vendors source and records it in cfg, replacing an entry of the
// same name. It returns the new lock and the previous one, if any.
func addPlugin(cfg *Config, source string) (lock, prev *PluginLock, err error) {
	lock, err = vendorPlugin(cfg.dir, source)
	if err != nil {
		return nil, nil, err
	}
	for i, p := range cfg.Plugins {
		if p.Name == lock.Name {
			cfg.Plugins[i] = *lock
			return lock, &p, nil
		}
	}
	cfg.Plugins = append(cfg.Plugins, *lock)
	sort.Slice(cfg.Plugins, func(i, j int) bool { return cfg.Plugins[i].Name < cfg.Plugins[j].Name })
	return lock, nil, nil
}

func describeLock(l *PluginLock) string {
	v := l.Version
	if v == "" {
		v = "unversioned"
	}
	return fmt.Sprintf("%s %s (sha256 %s)", l.Name, v, shorten(l.SHA256, 12))
}

func newPluginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Add, update and list vendored plugins",
		Long: `Plugins are libraries for the worker (e.g. iroh-wasm) described by a cfdo-plugin.json
in their source directory. cfdo copies their worker files into plugins/<name>/ and
their Claude Code skill into .claude/skills/<name>/, and records source and checksum
in cfdo.json. Uploads always ship the vendored copy; "plugin update" pulls a new one.`,
	}
	confPath := cmd.PersistentFlags().StringP("config", "c", "", "path to cfdo.json (default: nearest one up the tree)")

	add := &cobra.Command{
		Use:   "add <source-dir>",
		Short: "Vendor a plugin into this project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(*confPath)
			if err != nil {
				return err
			}
			lock, prev, err := addPlugin(cfg, args[0])
			if err != nil {
				return err
			}
			if err := cfg.save(cfg.path); err != nil {
				return err
			}
			if prev != nil {
				fmt.Printf("Replaced %s with %s\n", describeLock(prev), describeLock(lock))
			} else {
				fmt.Printf("Added %s\n", describeLock(lock))
			}
			return nil
		},
	}

	update := &cobra.Command{
		Use:   "update [name...]",
		Short: "Re-vendor plugins from their recorded source (all when no name is given)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(*confPath)
			if err != nil {
				return err
			}
			want := map[string]bool{}
			for _, a := range args {
				want[a] = true
			}
			var sources []string
			for _, p := range cfg.Plugins {
				if len(want) == 0 || want[p.Name] {
					sources = append(sources, p.Source)
					delete(want, p.Name)
				}
			}
			for name := range want {
				return fmt.Errorf("no plugin %q in %s", name, configName)
			}
			if len(sources) == 0 {
				fmt.Println("No plugins.")
				return nil
			}
			for _, src := range sources {
				lock, prev, err := addPlugin(cfg, src)
				if err != nil {
					return err
				}
				switch {
				case prev != nil && prev.SHA256 == lock.SHA256:
					fmt.Printf("%s: unchanged\n", describeLock(lock))
				case prev != nil:
					fmt.Printf("Updated %s -> %s\n", describeLock(prev), describeLock(lock))
				default:
					return fmt.Errorf("plugin at %s now calls itself %q; remove the old entry from %s and run `cfdo plugin add`", src, lock.Name, configName)
				}
			}
			return cfg.save(cfg.path)
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "Show vendored plugins",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig(*confPath)
			if err != nil {
				return err
			}
			if len(cfg.Plugins) == 0 {
				fmt.Println("No plugins.")
				return nil
			}
			for _, p := range cfg.Plugins {
				fmt.Printf("%s\n  from %s\n", describeLock(&p), p.Source)
			}
			return nil
		},
	}

	cmd.AddCommand(add, update, list)
	return cmd
}
