package main

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

//go:embed templates/worker.mjs
var workerTemplate string

//go:embed templates/skill.md
var skillTemplate string

var identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func cmdCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	dir := fs.String("dir", "", "directory to scaffold into (default: ./<name>)")
	account := fs.String("account", os.Getenv("CLOUDFLARE_ACCOUNT_ID"), "Cloudflare account id")
	class := fs.String("class", "", "Durable Object class name (default: derived from <name>)")
	binding := fs.String("binding", "", "environment binding name (default: derived from class)")
	kv := fs.Bool("kv", false, "use the legacy key-value backend instead of SQLite-backed storage")
	compat := fs.String("compat-date", time.Now().Format("2006-01-02"), "worker compatibility date")
	force := fs.Bool("force", false, "overwrite existing files")
	noSkill := fs.Bool("no-skill", false, "do not write the Claude Code skill into .claude/skills/")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, "Usage: cfdo create <script-name> [flags]\n\nScaffolds cfdo.json and a worker module with cfdo's admin routes.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(permute(fs, args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return fmt.Errorf("expected exactly one script name")
	}

	script := fs.Arg(0)
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(script) {
		return fmt.Errorf("script name %q must be lowercase alphanumeric with dashes", script)
	}
	if *class == "" {
		*class = pascal(script)
	}
	if !identRe.MatchString(*class) {
		return fmt.Errorf("class name %q is not a valid JavaScript identifier", *class)
	}
	if *binding == "" {
		*binding = strings.ToUpper(screamingSnake(*class))
	}
	if !identRe.MatchString(*binding) {
		return fmt.Errorf("binding name %q is not a valid identifier", *binding)
	}

	target := *dir
	if target == "" {
		target = script
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	cfg := &Config{
		AccountID:         *account,
		ScriptName:        script,
		ClassName:         *class,
		Binding:           *binding,
		MainModule:        "worker.mjs",
		CompatibilityDate: *compat,
		SQLite:            !*kv,
		MigrationTag:      "v1",
		WorkersDev:        true,
	}
	if cfg.AccountID == "" {
		cfg.AccountID = "REPLACE_WITH_ACCOUNT_ID"
	}

	worker := strings.NewReplacer(
		"__CLASS_NAME__", *class,
		"__BINDING__", *binding,
	).Replace(workerTemplate)

	files := map[string]string{
		filepath.Join(target, "worker.mjs"): worker,
		filepath.Join(target, ".gitignore"): ".cfdo/\nbackups/\n.env\n",
	}
	skillPath := filepath.Join(target, ".claude", "skills", script, "SKILL.md")
	if !*noSkill {
		files[skillPath] = renderSkill(script, *class, *binding, !*kv)
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := writeFile(path, []byte(content), *force); err != nil {
			return err
		}
	}
	cfgPath := filepath.Join(target, configName)
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		return fmt.Errorf("%s already exists (pass -force to overwrite)", cfgPath)
	}
	if err := cfg.save(cfgPath); err != nil {
		return err
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	secretStr := base64.RawURLEncoding.EncodeToString(secret)

	fmt.Printf("Created %s\n", target)
	fmt.Printf("  %-14s %s\n", configName, "project config")
	fmt.Printf("  %-14s %s class %s, binding %s\n", "worker.mjs", storageKind(cfg.SQLite), cfg.ClassName, cfg.Binding)
	if !*noSkill {
		fmt.Printf("  %-14s Claude Code skill: what DOs can do, how to implement and operate this one\n",
			filepath.Join(".claude", "skills", script))
	}
	fmt.Println()
	fmt.Println("Next:")
	if cfg.AccountID == "REPLACE_WITH_ACCOUNT_ID" {
		fmt.Printf("  1. set account_id in %s (or export CLOUDFLARE_ACCOUNT_ID)\n", filepath.Join(target, configName))
	} else {
		fmt.Println("  1. export CLOUDFLARE_API_TOKEN=<token with Workers Scripts:Edit>")
	}
	fmt.Printf("  2. export CFDO_SECRET=%s\n", secretStr)
	fmt.Printf("  3. cd %s && cfdo upload\n", target)
	fmt.Println()
	fmt.Println("Keep CFDO_SECRET: it is uploaded as a worker secret and is required by backup/restore.")
	return nil
}

func writeFile(path string, data []byte, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (pass -force to overwrite)", path)
		}
	}
	return os.WriteFile(path, data, 0o644)
}

func storageKind(sqlite bool) string {
	if sqlite {
		return "SQLite-backed"
	}
	return "key-value"
}

func pascal(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' || r == ' ' })
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	if b.Len() == 0 {
		return "DurableObject"
	}
	return b.String()
}

func screamingSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(s[i-1])
			if prev < 'A' || prev > 'Z' {
				b.WriteByte('_')
			}
		}
		b.WriteRune(r)
	}
	return strings.ToUpper(b.String())
}

// renderSkill fills in the project's Claude Code skill, including the sections
// that only apply to one storage backend.
func renderSkill(script, class, binding string, sqlite bool) string {
	capabilities := kvCapabilities
	note := kvStorageNote
	if sqlite {
		capabilities = sqliteCapabilities
		note = sqliteStorageNote
	}
	return strings.NewReplacer(
		"__SCRIPT_NAME__", script,
		"__CLASS_NAME__", class,
		"__BINDING__", binding,
		"__STORAGE__", storageKind(sqlite),
		"__STORAGE_CAPABILITIES__", capabilities,
		"__STORAGE_NOTE__", note,
	).Replace(skillTemplate)
}

const sqliteCapabilities = `This class is **SQLite-backed**, which adds:

- **A real SQL database per object.** ` + "`ctx.storage.sql.exec(query, ...params)`" + ` returns an
  iterable cursor. Use bound parameters, never string interpolation. Indexes, joins and
  aggregates all work; it is SQLite, scoped to this one object.
- **The key-value API as well.** ` + "`ctx.storage.get/put/list`" + ` still works alongside SQL, which
  suits small scalars (a counter, a config flag) that do not deserve a table.
- **Point-in-time recovery.** Storage bookmarks let an object be restored to an earlier
  moment within the retention window — independent of, and complementary to, ` + "`cfdo backup`" + `.
  cfdo gives you a portable copy you can inspect and diff; PITR gives you a rewind.`

const kvCapabilities = `This class uses the **key-value backend**, which means:

- **` + "`ctx.storage.get/put/delete/list`" + ` only** — there is no SQL API. Values are
  structured-clonable (objects, ` + "`Map`" + `, ` + "`Set`" + `, ` + "`Date`" + `, ` + "`ArrayBuffer`" + `), not just JSON.
- **` + "`list()`" + ` is ordered by key**, so a key prefix like ` + "`msg:00000042`" + ` is how you get
  range scans and pagination. Design keys for the queries you need up front.
- **New projects should prefer SQLite-backed storage** unless something specific requires
  this backend; it is the direction Cloudflare is going, and it is what enables SQL and
  point-in-time recovery. Switching an existing class is a migration, not a flag flip.`

const sqliteStorageNote = `**Schema.** It lives in the constructor as ` + "`CREATE TABLE IF NOT EXISTS`" + `, so each
object self-initialises on first use. Objects are created lazily over time, so old and new
ones coexist: make schema changes additive and idempotent (a guarded ` + "`ALTER TABLE`" + `, or a
version row you check on open) rather than assuming every object is current.`

const kvStorageNote = `**Keys are the only index**, so choose them for the reads you need:
` + "`user:<id>`" + ` for point lookups, a zero-padded suffix (` + "`msg:00000042`" + `) where ` + "`list()`" + `
order matters. There is no way to query by value — a second key that points at the first is
how you get a lookup by another field.`
