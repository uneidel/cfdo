package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeWorker stands in for a deployed worker running the generated module.
type fakeWorker struct {
	mu      sync.Mutex
	storage map[string]map[string]any // object id -> exported body
	index   []indexEntry              // what the worker's index object knows
	secret  string
}

func (w *fakeWorker) handler() http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-cfdo-secret") != w.secret {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		id := r.URL.Query().Get("id")
		w.mu.Lock()
		defer w.mu.Unlock()

		switch r.URL.Path {
		case "/__cfdo/ping":
			json.NewEncoder(rw).Encode(map[string]any{"ok": true, "class": "ChatRoom", "format": 1})
		case "/__cfdo/list":
			json.NewEncoder(rw).Encode(map[string]any{"objects": w.index})
		case "/__cfdo/export":
			body, ok := w.storage[id]
			if !ok {
				http.Error(rw, "no such object", http.StatusNotFound)
				return
			}
			json.NewEncoder(rw).Encode(body)
		case "/__cfdo/import":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			w.storage[id] = body
			kv, _ := body["kv"].([]any)
			json.NewEncoder(rw).Encode(map[string]any{
				"ok": true, "mode": r.URL.Query().Get("mode"), "kv": len(kv), "rows": 0,
			})
		default:
			http.Error(rw, "not found", http.StatusNotFound)
		}
	})
}

// fakeAPI serves the handful of Cloudflare endpoints cfdo calls, including a
// paginated object listing.
func fakeAPI(t *testing.T, objectIDs []string) *httptest.Server {
	t.Helper()
	ok := func(rw http.ResponseWriter, result any, info map[string]any) {
		env := map[string]any{"success": true, "errors": []any{}, "messages": []any{}, "result": result}
		if info != nil {
			env["result_info"] = info
		}
		json.NewEncoder(rw).Encode(env)
	}
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			rw.WriteHeader(http.StatusForbidden)
			json.NewEncoder(rw).Encode(map[string]any{"success": false,
				"errors": []APIError{{Code: 10000, Message: "Authentication error"}}})
			return
		}
		switch r.URL.Path {
		case "/accounts/acct/workers/durable_objects/namespaces":
			ok(rw, []Namespace{{ID: "ns1", Name: "chat-room-ChatRoom", Script: "chat-room", Class: "ChatRoom", UseSQLite: true}}, nil)
		case "/accounts/acct/workers/durable_objects/namespaces/ns1/objects":
			// hand out one object per page to exercise cursor paging
			start := 0
			if c := r.URL.Query().Get("cursor"); c != "" {
				start, _ = strconv.Atoi(c)
			}
			if start >= len(objectIDs) {
				ok(rw, []DOObject{}, map[string]any{"cursor": ""})
				return
			}
			page := []DOObject{{ID: objectIDs[start], HasStoredData: true}}
			next := ""
			if start+1 < len(objectIDs) {
				next = strconv.Itoa(start + 1)
			}
			ok(rw, page, map[string]any{"cursor": next})
		case "/accounts/acct/workers/subdomain":
			ok(rw, map[string]string{"subdomain": "example"}, nil)
		default:
			t.Logf("unexpected API call: %s %s", r.Method, r.URL.Path)
			rw.WriteHeader(http.StatusNotFound)
			json.NewEncoder(rw).Encode(map[string]any{"success": false,
				"errors": []APIError{{Code: 404, Message: "not found"}}})
		}
	}))
}

func setupProject(t *testing.T, workerURL string) string {
	t.Helper()
	// Keep tests away from the developer's real ~/.cfdo/settings.json.
	t.Setenv("CFDO_HOME", t.TempDir())
	dir := t.TempDir()
	cfg := &Config{
		AccountID: "acct", ScriptName: "chat-room", ClassName: "ChatRoom",
		Binding: "CHAT_ROOM", MainModule: "worker.mjs", CompatibilityDate: "2026-09-01",
		SQLite: true, MigrationTag: "v1", WorkerURL: workerURL,
	}
	if err := cfg.save(filepath.Join(dir, configName)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBackupAndRestoreRoundTrip(t *testing.T) {
	ids := []string{"0a1b", "2c3d", "4e5f"}
	worker := &fakeWorker{secret: "s3cret", storage: map[string]map[string]any{}}
	for i, id := range ids {
		worker.index = append(worker.index, indexEntry{Name: fmt.Sprintf("room-%d", i), ID: id})
		worker.storage[id] = map[string]any{
			"format": 1.0, "class": "ChatRoom",
			"kv": []any{[]any{"count", float64(i)}, []any{"name", fmt.Sprintf("room-%d", i)}},
		}
	}
	ws := httptest.NewServer(worker.handler())
	defer ws.Close()
	api := fakeAPI(t, ids)
	defer api.Close()

	dir := setupProject(t, ws.URL)
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CFDO_SECRET", "s3cret")

	conf := filepath.Join(dir, configName)
	out := filepath.Join(dir, "bk")
	if err := execute(context.Background(), "backup", "-c", conf, "-o", out); err != nil {
		t.Fatalf("backup: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if len(man.Objects) != len(ids) {
		t.Fatalf("manifest has %d objects, want %d (pagination lost some?)", len(man.Objects), len(ids))
	}
	if man.NamespaceID != "ns1" || !man.SQLite {
		t.Fatalf("manifest metadata wrong: %+v", man)
	}
	for _, o := range man.Objects {
		if o.SHA256 == "" || o.Bytes == 0 {
			t.Fatalf("object %s missing checksum/size", o.ID)
		}
	}

	// wipe the "live" objects, then restore them
	worker.mu.Lock()
	worker.storage = map[string]map[string]any{}
	worker.mu.Unlock()

	if err := execute(context.Background(), "restore", "-c", conf, "-y", "--mode", "replace", out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if len(worker.storage) != len(ids) {
		t.Fatalf("restored %d objects, want %d", len(worker.storage), len(ids))
	}
	got := worker.storage["2c3d"]
	kv, _ := got["kv"].([]any)
	if len(kv) != 2 {
		t.Fatalf("restored object 2c3d has %d keys, want 2: %+v", len(kv), got)
	}
}

func TestRestoreDetectsTamperedFile(t *testing.T) {
	ids := []string{"0a1b"}
	worker := &fakeWorker{secret: "s3cret", storage: map[string]map[string]any{
		"0a1b": {"format": 1.0, "kv": []any{}},
	}}
	ws := httptest.NewServer(worker.handler())
	defer ws.Close()
	api := fakeAPI(t, ids)
	defer api.Close()

	dir := setupProject(t, ws.URL)
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CFDO_SECRET", "s3cret")

	conf := filepath.Join(dir, configName)
	out := filepath.Join(dir, "bk")
	if err := execute(context.Background(), "backup", "-c", conf, "-o", out); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "objects", "0a1b.json"), []byte(`{"format":1,"kv":[]}  `), 0o600); err != nil {
		t.Fatal(err)
	}
	err := execute(context.Background(), "restore", "-c", conf, "-y", out)
	if err == nil {
		t.Fatal("expected restore to reject a modified object file")
	}
}

func TestBackupRejectsWrongSecret(t *testing.T) {
	ids := []string{"0a1b"}
	worker := &fakeWorker{secret: "right", storage: map[string]map[string]any{"0a1b": {"format": 1.0}}}
	ws := httptest.NewServer(worker.handler())
	defer ws.Close()
	api := fakeAPI(t, ids)
	defer api.Close()

	dir := setupProject(t, ws.URL)
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CFDO_SECRET", "wrong")

	err := execute(context.Background(), "backup", "-c", filepath.Join(dir, configName), "-o", filepath.Join(dir, "bk"))
	if err == nil {
		t.Fatal("expected backup to fail on secret mismatch")
	}
}

func TestAPIErrorsSurfaceCloudflareCodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.WriteHeader(http.StatusBadRequest)
		io.WriteString(rw, `{"success":false,"errors":[{"code":10061,"message":"Durable Object class already exists"}]}`)
	}))
	defer srv.Close()

	c := NewClient("t")
	c.Base = srv.URL
	_, err := c.ListNamespaces(context.Background(), "acct")
	var he *HTTPError
	if !asHTTPError(err, &he) {
		t.Fatalf("want *HTTPError, got %T: %v", err, err)
	}
	if !he.has(10061) {
		t.Fatalf("error lost the Cloudflare code: %v", err)
	}
}

func TestPlanMigration(t *testing.T) {
	cfg := &Config{ClassName: "ChatRoom", SQLite: true, MigrationTag: "v1"}

	m, _ := planMigration(cfg, &State{}, "v1", nil, nil, nil)
	if m == nil || m.OldTag != "" || len(m.NewSQLiteClasses) != 1 {
		t.Fatalf("first upload should create the class with no old_tag: %+v", m)
	}
	if m, _ := planMigration(cfg, &State{AppliedMigrationTag: "v1"}, "v1", nil, nil, nil); m != nil {
		t.Fatalf("re-uploading the same tag should send no migration: %+v", m)
	}
	m, _ = planMigration(cfg, &State{AppliedMigrationTag: "v1"}, "v2", nil, nil, nil)
	if m.OldTag != "v1" || m.NewTag != "v2" || len(m.NewSQLiteClasses) != 0 {
		t.Fatalf("a tag bump must not re-create the class: %+v", m)
	}
	kv := &Config{ClassName: "ChatRoom", SQLite: false, MigrationTag: "v1"}
	m, _ = planMigration(kv, &State{}, "v1", nil, nil, nil)
	if len(m.NewClasses) != 1 || len(m.NewSQLiteClasses) != 0 {
		t.Fatalf("non-sqlite project must use new_classes: %+v", m)
	}
}

func TestRedactSecretsDoesNotMangleMetadata(t *testing.T) {
	meta := map[string]any{
		"main_module": "worker.mjs",
		"bindings": []map[string]any{
			{"type": "durable_object_namespace", "name": "CHAT_ROOM", "class_name": "ChatRoom"},
			{"type": "secret_text", "name": "CFDO_SECRET", "text": "s"},
		},
	}
	b, err := json.Marshal(redactSecrets(meta))
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("redacted metadata is not valid JSON: %v", err)
	}
	if back["main_module"] != "worker.mjs" {
		t.Fatalf("redaction corrupted unrelated fields: %s", b)
	}
	bindings := back["bindings"].([]any)
	if got := bindings[1].(map[string]any)["text"]; got == "s" {
		t.Fatal("secret was not redacted")
	}
	// the original must be untouched, since it is what actually gets uploaded
	if meta["bindings"].([]map[string]any)[1]["text"] != "s" {
		t.Fatal("redaction mutated the metadata that gets uploaded")
	}
}

// A SQLite-backed namespace returns nothing from the Cloudflare listing API,
// so the worker's index has to be what finds the objects.
func TestBackupDiscoversViaWorkerIndexWhenAPIListsNothing(t *testing.T) {
	worker := &fakeWorker{secret: "s3cret", storage: map[string]map[string]any{
		"aa": {"format": 1.0, "kv": []any{[]any{"k", "v"}}},
		"bb": {"format": 1.0, "kv": []any{}},
	}, index: []indexEntry{{Name: "alpha", ID: "aa"}, {Name: "beta", ID: "bb"}}}
	ws := httptest.NewServer(worker.handler())
	defer ws.Close()
	api := fakeAPI(t, nil) // API reports zero objects, as it does for SQLite
	defer api.Close()

	dir := setupProject(t, ws.URL)
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CFDO_SECRET", "s3cret")

	out := filepath.Join(dir, "bk")
	if err := execute(context.Background(), "backup", "-c", filepath.Join(dir, configName), "-o", out); err != nil {
		t.Fatalf("backup: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(out, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var man Manifest
	if err := json.Unmarshal(raw, &man); err != nil {
		t.Fatal(err)
	}
	if len(man.Objects) != 2 {
		t.Fatalf("index discovery found %d objects, want 2", len(man.Objects))
	}
	names := map[string]string{}
	for _, o := range man.Objects {
		names[o.ID] = o.Name
	}
	if names["aa"] != "alpha" || names["bb"] != "beta" {
		t.Fatalf("manifest lost the routing names: %+v", names)
	}
}

// Ids the index cannot know (newUniqueId) come in through -ids-file, and must
// not be duplicated when another source already reported them.
func TestBackupIdsFileMergesWithoutDuplicates(t *testing.T) {
	worker := &fakeWorker{secret: "s3cret", storage: map[string]map[string]any{
		"aa": {"format": 1.0}, "cc": {"format": 1.0},
	}, index: []indexEntry{{Name: "alpha", ID: "aa"}}}
	ws := httptest.NewServer(worker.handler())
	defer ws.Close()
	api := fakeAPI(t, nil)
	defer api.Close()

	dir := setupProject(t, ws.URL)
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "test-token")
	t.Setenv("CFDO_SECRET", "s3cret")

	idsFile := filepath.Join(dir, "ids.txt")
	if err := os.WriteFile(idsFile, []byte("# extra objects\naa\ncc\n\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "bk")
	if err := execute(context.Background(), "backup", "-c", filepath.Join(dir, configName), "-o", out, "--ids-file", idsFile); err != nil {
		t.Fatalf("backup: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(out, "manifest.json"))
	var man Manifest
	json.Unmarshal(raw, &man)
	if len(man.Objects) != 2 {
		t.Fatalf("want 2 unique objects, got %d: %+v", len(man.Objects), man.Objects)
	}
	for _, o := range man.Objects {
		if o.ID == "aa" && o.Name != "alpha" {
			t.Fatal("ids-file entry clobbered the name from the index")
		}
	}
}

func TestFlagsMayFollowPositionalArgs(t *testing.T) {
	create := newCreateCmd()
	if err := create.ParseFlags([]string{"my-do", "--force", "--dir", "out", "--kv=true"}); err != nil {
		t.Fatal(err)
	}
	if force, _ := create.Flags().GetBool("force"); !force {
		t.Fatal("--force after a positional was dropped")
	}
	if dir, _ := create.Flags().GetString("dir"); dir != "out" {
		t.Fatalf("--dir after a positional was dropped: %q", dir)
	}
	if args := create.Flags().Args(); len(args) != 1 || args[0] != "my-do" {
		t.Fatalf("positional lost: %v", args)
	}

	// a value that looks like a positional must stay attached to its flag
	restore := newRestoreCmd()
	if err := restore.ParseFlags([]string{"backupdir", "--only", "abc123", "--mode=replace"}); err != nil {
		t.Fatal(err)
	}
	only, _ := restore.Flags().GetString("only")
	mode, _ := restore.Flags().GetString("mode")
	if args := restore.Flags().Args(); only != "abc123" || mode != "replace" || len(args) != 1 || args[0] != "backupdir" {
		t.Fatalf("flag value was treated as positional: only=%q mode=%q args=%v", only, mode, args)
	}
}

func TestSettingsResolutionPrecedence(t *testing.T) {
	s := &Settings{
		APIToken:  "file-token",
		AccountID: "file-account",
		Secret:    "file-default-secret",
		Scripts:   map[string]ScriptEntry{"guestbook": {Secret: "guestbook-secret"}},
	}

	// environment wins over the file
	t.Setenv("CLOUDFLARE_API_TOKEN", "env-token")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "env-account")
	t.Setenv("CFDO_SECRET", "env-secret")
	if got, _ := resolveAPIToken(s); got != "env-token" {
		t.Fatalf("token: env must win, got %q", got)
	}
	if got := resolveAccountID(s, "cfdo-json-account"); got != "env-account" {
		t.Fatalf("account: env must win, got %q", got)
	}
	if got, _ := resolveSecret(s, "guestbook"); got != "env-secret" {
		t.Fatalf("secret: env must win, got %q", got)
	}

	// without the environment, the project's cfdo.json beats the user-wide file
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CFDO_SECRET", "")
	if got := resolveAccountID(s, "cfdo-json-account"); got != "cfdo-json-account" {
		t.Fatalf("account: cfdo.json must beat settings, got %q", got)
	}
	if got := resolveAccountID(s, ""); got != "file-account" {
		t.Fatalf("account: settings must be the last fallback, got %q", got)
	}
	if got := resolveAccountID(s, accountPlaceholder); got != "file-account" {
		t.Fatalf("account: the scaffold placeholder must not count as a value, got %q", got)
	}
	if got, _ := resolveAPIToken(s); got != "file-token" {
		t.Fatalf("token: want the file value, got %q", got)
	}

	// a per-script secret beats the global one; other scripts fall back
	if got, _ := resolveSecret(s, "guestbook"); got != "guestbook-secret" {
		t.Fatalf("secret: per-script must win, got %q", got)
	}
	if got, _ := resolveSecret(s, "other"); got != "file-default-secret" {
		t.Fatalf("secret: unknown script should fall back to the default, got %q", got)
	}
	if _, err := resolveSecret(&Settings{}, "any"); err == nil {
		t.Fatal("expected an error when no secret is available anywhere")
	}
}

func TestInitWritesPrivateSettingsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CFDO_HOME", home)
	t.Setenv("CLOUDFLARE_API_TOKEN", "")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	t.Setenv("CFDO_SECRET", "")

	err := execute(context.Background(), "init", "--token", "tok-abc", "--account", "acct-123",
		"--secret", "sec-xyz", "--script", "cfdo-guestbook", "--no-verify")
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	path := filepath.Join(home, ".cfdo", "settings.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("settings.json is mode %o, want 600 — it holds an API token", mode)
	}
	if dirInfo, err := os.Stat(filepath.Join(home, ".cfdo")); err == nil {
		if mode := dirInfo.Mode().Perm(); mode != 0o700 {
			t.Fatalf(".cfdo dir is mode %o, want 700", mode)
		}
	}

	loaded, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.APIToken != "tok-abc" || loaded.AccountID != "acct-123" {
		t.Fatalf("settings did not round-trip: %+v", loaded)
	}
	if loaded.Scripts["cfdo-guestbook"].Secret != "sec-xyz" {
		t.Fatalf("-script did not scope the secret: %+v", loaded.Scripts)
	}
	if loaded.Secret != "" {
		t.Fatal("-script should not also set the global secret")
	}

	// a second init must not clobber what is already recorded
	if err := execute(context.Background(), "init", "--account", "acct-999", "--no-verify"); err != nil {
		t.Fatalf("second init: %v", err)
	}
	again, _ := loadSettings()
	if again.APIToken != "tok-abc" {
		t.Fatalf("re-running init dropped the token: %+v", again)
	}
	if again.AccountID != "acct-999" {
		t.Fatalf("explicit -account should have updated the value, got %q", again.AccountID)
	}
}

func TestMissingSettingsFileIsNotAnError(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	s, err := loadSettings()
	if err != nil {
		t.Fatalf("a missing settings file must not be an error: %v", err)
	}
	if s.APIToken != "" || s.Scripts != nil {
		t.Fatalf("want empty settings, got %+v", s)
	}
}

func TestMaskNeverRevealsFullCredential(t *testing.T) {
	for _, v := range []string{"", "short", "cfat_ZX3TESmPufzps44zTTV0eXaNrwMZdBG43O0"} {
		got := mask(v)
		if v != "" && got == v {
			t.Fatalf("mask(%q) returned it unchanged", v)
		}
		if len(v) > 8 && strings.Contains(got, v[4:len(v)-4]) {
			t.Fatalf("mask(%q) leaked the middle: %q", v, got)
		}
	}
}

func TestCreateWritesSkill(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	dir := t.TempDir()

	if err := execute(context.Background(), "create", "inbox-sync", "--dir", dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".claude", "skills", "inbox-sync", "SKILL.md")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("create did not write the skill: %v", err)
	}
	body := string(raw)

	// frontmatter must be the first thing in the file and carry name+description
	if !strings.HasPrefix(body, "---\n") {
		t.Fatal("SKILL.md must open with YAML frontmatter")
	}
	end := strings.Index(body[4:], "\n---\n")
	if end < 0 {
		t.Fatal("frontmatter is not terminated")
	}
	front := body[4 : end+4]
	for _, key := range []string{"name: inbox-sync", "description: "} {
		if !strings.Contains(front, key) {
			t.Fatalf("frontmatter missing %q: %q", key, front)
		}
	}
	// the description must be one line, or the YAML is invalid
	for _, line := range strings.Split(front, "\n") {
		if strings.HasPrefix(line, "description:") && len(line) < 40 {
			t.Fatalf("description looks truncated: %q", line)
		}
	}

	// every placeholder must be substituted, including the storage-specific ones
	if left := regexp.MustCompile(`__[A-Z][A-Z_]*__`).FindAllString(body, -1); left != nil {
		t.Fatalf("unsubstituted placeholders in SKILL.md: %v", left)
	}
	for _, want := range []string{"InboxSync", "INBOX_SYNC", "inbox-sync", "SQLite-backed"} {
		if !strings.Contains(body, want) {
			t.Fatalf("SKILL.md never mentions %q", want)
		}
	}
	// the storage note is a separate template slot; it has silently gone
	// missing before, so assert its content actually landed
	if !strings.Contains(body, "**Schema.**") {
		t.Fatal("the SQLite storage note did not make it into the skill")
	}
	if strings.Contains(body, "Keys are the only index") {
		t.Fatal("a SQLite project got the key-value storage note")
	}
}

func TestCreateKVSkillMakesNoSQLPromises(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	dir := t.TempDir()

	if err := execute(context.Background(), "create", "legacy-kv", "--dir", dir, "--kv"); err != nil {
		t.Fatal(err)
	}
	body := mustRead(t, filepath.Join(dir, ".claude", "skills", "legacy-kv", "SKILL.md"))

	if !strings.Contains(body, "key-value") {
		t.Fatal("KV project's skill does not say it is key-value backed")
	}
	if strings.Contains(body, "**Schema.**") {
		t.Fatal("a key-value project got the SQLite schema note")
	}
	// it may mention SQL only to recommend it, never as an available API here
	if strings.Contains(body, "ctx.storage.sql.exec") {
		t.Fatal("KV skill documents a SQL API the class does not have")
	}
}

func TestCreateNoSkillFlag(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")
	dir := t.TempDir()

	if err := execute(context.Background(), "create", "plain", "--dir", dir, "--no-skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !os.IsNotExist(err) {
		t.Fatal("-no-skill should not create .claude/")
	}
	if _, err := os.Stat(filepath.Join(dir, "worker.mjs")); err != nil {
		t.Fatal("-no-skill must still scaffold the project")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// listAPI serves the endpoints `cfdo list` needs: several namespaces across two
// scripts, one of which is not deployed.
func listAPI(t *testing.T) *httptest.Server {
	t.Helper()
	ok := func(rw http.ResponseWriter, result any, info map[string]any) {
		env := map[string]any{"success": true, "errors": []any{}, "result": result}
		if info != nil {
			env["result_info"] = info
		}
		json.NewEncoder(rw).Encode(env)
	}
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/accounts/acct/workers/durable_objects/namespaces":
			ok(rw, []Namespace{
				{ID: "ns-b", Script: "zeta-app", Class: "Room", UseSQLite: true},
				{ID: "ns-a", Script: "alpha-app", Class: "Counter", UseSQLite: false},
				{ID: "ns-c", Script: "alpha-app", Class: "Archive", UseSQLite: true},
			}, nil)
		case r.URL.Path == "/accounts/acct/workers/scripts":
			// zeta-app has a namespace but is not in the script list
			ok(rw, []ScriptInfo{{ID: "alpha-app", ModifiedOn: "2026-09-01T00:00:00Z"}}, nil)
		case strings.HasSuffix(r.URL.Path, "/objects"):
			if r.URL.Query().Get("cursor") != "" {
				ok(rw, []DOObject{}, map[string]any{"cursor": ""})
				return
			}
			n := map[string]int{"ns-a": 2, "ns-b": 0, "ns-c": 1}[pathSegment(r.URL.Path, 5)]
			objs := make([]DOObject, 0, n)
			for i := 0; i < n; i++ {
				objs = append(objs, DOObject{ID: fmt.Sprintf("obj%d", i), HasStoredData: true})
			}
			ok(rw, objs, map[string]any{"cursor": ""})
		default:
			rw.WriteHeader(http.StatusNotFound)
			json.NewEncoder(rw).Encode(map[string]any{"success": false,
				"errors": []APIError{{Code: 404, Message: "not found: " + r.URL.Path}}})
		}
	}))
}

func pathSegment(path string, i int) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if i < len(parts) {
		return parts[i]
	}
	return ""
}

func TestListSortsAndMarksUndeployed(t *testing.T) {
	api := listAPI(t)
	defer api.Close()
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")

	out := captureStdout(t, func() {
		if err := execute(context.Background(), "list", "--json"); err != nil {
			t.Fatalf("list: %v", err)
		}
	})

	var got struct {
		AccountID  string            `json:"account_id"`
		Namespaces []namespaceReport `json:"namespaces"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("list -json did not emit valid JSON: %v\n%s", err, out)
	}
	if got.AccountID != "acct" || len(got.Namespaces) != 3 {
		t.Fatalf("want 3 namespaces for acct, got %+v", got)
	}
	// sorted by script, then class
	wantOrder := [][2]string{{"alpha-app", "Archive"}, {"alpha-app", "Counter"}, {"zeta-app", "Room"}}
	for i, w := range wantOrder {
		if got.Namespaces[i].Script != w[0] || got.Namespaces[i].Class != w[1] {
			t.Fatalf("row %d is %s/%s, want %s/%s", i,
				got.Namespaces[i].Script, got.Namespaces[i].Class, w[0], w[1])
		}
	}
	for _, r := range got.Namespaces {
		if r.Script == "zeta-app" && r.Deployed {
			t.Fatal("zeta-app has no script entry and must not be marked deployed")
		}
		if r.Script == "alpha-app" && !r.Deployed {
			t.Fatal("alpha-app should be marked deployed")
		}
	}
	// object counts come through per namespace
	counts := map[string]int{}
	for _, r := range got.Namespaces {
		counts[r.NamespaceID] = r.APIObjects
	}
	if counts["ns-a"] != 2 || counts["ns-c"] != 1 || counts["ns-b"] != 0 {
		t.Fatalf("object counts wrong: %v", counts)
	}
}

func TestListScriptFilter(t *testing.T) {
	api := listAPI(t)
	defer api.Close()
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CFDO_API_BASE", api.URL)
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "acct")

	out := captureStdout(t, func() {
		if err := execute(context.Background(), "list", "--json", "--script", "zeta-app"); err != nil {
			t.Fatal(err)
		}
	})
	var got struct {
		Namespaces []namespaceReport `json:"namespaces"`
	}
	json.Unmarshal([]byte(out), &got)
	if len(got.Namespaces) != 1 || got.Namespaces[0].Script != "zeta-app" {
		t.Fatalf("-script filter did not apply: %+v", got.Namespaces)
	}
}

func TestListNeedsAnAccountID(t *testing.T) {
	t.Setenv("CFDO_HOME", t.TempDir())
	t.Setenv("CLOUDFLARE_API_TOKEN", "tok")
	t.Setenv("CLOUDFLARE_ACCOUNT_ID", "")
	err := execute(context.Background(), "list")
	if err == nil || !strings.Contains(err.Error(), "no account id") {
		t.Fatalf("want a clear account-id error, got %v", err)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = saved
	return <-done
}
