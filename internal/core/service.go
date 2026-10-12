package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Service is the UI-agnostic seam the native Fyne view builds against. Reads
// return structured Go values; mutating actions return a *Job that streams the
// command's output over a channel and can be cancelled (Delete is synchronous).
// The dependency DAG is strict: cmd → gui → core; core imports neither gui nor
// cmd, has no net/http and no SSE.
//
// The implementation runs cfdo's own command logic in this process, so
// credentials resolve exactly as they do for the CLI (same env, same cwd), and
// credential values are masked in-process before they ever reach the view.
type Service interface {
	// Reads.
	Namespaces(ctx context.Context, req NamespacesRequest) ([]NamespaceReport, error)
	Status(ctx context.Context, req StatusRequest) (StatusReport, error)
	Settings(ctx context.Context, script string) (string, error)
	Backups(ctx context.Context) ([]BackupEntry, error)
	Plugins(ctx context.Context) ([]PluginLock, error)

	// Synchronous action.
	Delete(ctx context.Context, req DeleteRequest) error

	// Streaming actions. A validation error returns a nil *Job and nothing
	// runs; otherwise the Job streams Events and the caller reads the outcome
	// from the terminal Event (Final=true, Err set), after which Events closes.
	Backup(ctx context.Context, req BackupRequest) (*Job, error)
	Restore(ctx context.Context, req RestoreRequest) (*Job, error)
	Upload(ctx context.Context, req UploadRequest) (*Job, error)
	Create(ctx context.Context, req CreateRequest) (*Job, error)
	Plugin(ctx context.Context, req PluginRequest) (*Job, error)
}

// service is the concrete Service.
type service struct {
	mask *secretMasker
}

// NewService builds a Service. The returned value is cheap; make one per app.
func NewService() Service {
	return &service{mask: newSecretMasker(resolvedCredentials()...)}
}

// resolvedCredentials gathers every credential value the masker should scrub
// from streamed output. It seeds from the RESOLVED creds (the same values the
// commands actually use), not the environment alone: a secret recorded only in
// ~/.cfdo/settings.json — never exported as CFDO_SECRET — must still be masked
// if it surfaces in a line. We collect the global API token and secret plus
// each known script's resolved secret, then add the raw env values as a belt-
// and-braces fallback. Loading settings is best-effort: a missing or malformed
// file is not fatal here, it just means we mask whatever the environment gives.
func resolvedCredentials() []string {
	// Env values first, so masking still works even if settings fail to load.
	vals := []string{os.Getenv("CFDO_SECRET"), os.Getenv("CLOUDFLARE_API_TOKEN")}

	settings, err := loadSettings()
	if err != nil {
		return vals // best-effort: env-only masking
	}
	if tok, err := resolveAPIToken(settings); err == nil {
		vals = append(vals, tok)
	}
	// The global secret (empty script resolves to env or the shared secret).
	if sec, err := resolveSecret(settings, ""); err == nil {
		vals = append(vals, sec)
	}
	// Each script may carry its own --custom-secret.
	for script := range settings.Scripts {
		if sec, err := resolveSecret(settings, script); err == nil {
			vals = append(vals, sec)
		}
	}
	return vals
}

// ---- reads -------------------------------------------------------------

// NamespacesRequest selects which namespaces to report. All fields optional.
type NamespacesRequest struct {
	Account string // Cloudflare account id; empty resolves like the CLI
	Script  string // only namespaces for this script
	Objects bool   // include per-namespace object ids
	Deep    bool   // also ask each worker's index for its count
}

// Namespaces mirrors `cfdo list`: the Durable Object namespaces on the account.
func (s *service) Namespaces(ctx context.Context, req NamespacesRequest) ([]NamespaceReport, error) {
	_, rows, _, err := collectNamespaces(ctx, req.Account, req.Script, req.Objects, req.Deep)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// StatusReport is the typed status report. Known fields cover both the
// local-project form and the remote (script/--ns) form; a field is zero/nil
// when that form omits it. Extra carries any key a future cfdo version adds that
// this struct does not yet name, stringified, so nothing is ever silently
// dropped.
type StatusReport struct {
	Script              string            `json:"script"`
	AccountID           string            `json:"account_id,omitempty"`
	Class               string            `json:"class,omitempty"`
	Binding             string            `json:"binding,omitempty"`
	SQLite              *bool             `json:"sqlite,omitempty"`
	Deployed            bool              `json:"deployed"`
	ModifiedOn          string            `json:"modified_on,omitempty"`
	CompatibilityDate   string            `json:"compatibility_date,omitempty"`
	LocalMigrationTag   string            `json:"local_migration_tag,omitempty"`
	AppliedMigrationTag string            `json:"applied_migration_tag,omitempty"`
	RemoteMigrationTag  string            `json:"remote_migration_tag,omitempty"`
	LastUploadedAt      string            `json:"last_uploaded_at,omitempty"`
	Bindings            []string          `json:"bindings,omitempty"`
	NamespaceID         string            `json:"namespace_id,omitempty"`
	NamespaceSQLite     *bool             `json:"namespace_sqlite,omitempty"`
	NamespaceError      string            `json:"namespace_error,omitempty"`
	ObjectCount         *int              `json:"object_count,omitempty"`
	ObjectsWithData     *int              `json:"objects_with_data,omitempty"`
	ObjectsError        string            `json:"objects_error,omitempty"`
	ObjectIDs           []string          `json:"object_ids,omitempty"`
	WorkerURL           string            `json:"worker_url,omitempty"`
	Admin               string            `json:"admin,omitempty"`
	IndexedObjects      *int              `json:"indexed_objects,omitempty"`
	Index               []IndexEntry      `json:"index,omitempty"`
	Namespaces          []RemoteNamespace `json:"namespaces,omitempty"`
	Extra               map[string]string `json:"extra,omitempty"`
}

// knownStatusKeys is every json key the StatusReport struct names; anything else
// in a report lands in Extra.
var knownStatusKeys = map[string]bool{
	"script": true, "account_id": true, "class": true, "binding": true,
	"sqlite": true, "deployed": true, "modified_on": true, "compatibility_date": true,
	"local_migration_tag": true, "applied_migration_tag": true, "remote_migration_tag": true,
	"last_uploaded_at": true, "bindings": true, "namespace_id": true, "namespace_sqlite": true,
	"namespace_error": true, "object_count": true, "objects_with_data": true, "objects_error": true,
	"object_ids": true, "worker_url": true, "admin": true, "indexed_objects": true,
	"index": true, "namespaces": true,
}

// StatusRequest selects a status target. With Script or NS set it reports on any
// worker on the account (Config must be empty then); otherwise it reports on the
// local project in Config (or the nearest cfdo.json).
type StatusRequest struct {
	Script  string
	NS      string
	Account string
	Config  string
	Objects bool
	NoPing  bool
}

// Status mirrors `cfdo status`.
func (s *service) Status(ctx context.Context, req StatusRequest) (StatusReport, error) {
	var report map[string]any
	if req.Script != "" || req.NS != "" {
		if req.Config != "" {
			return StatusReport{}, fmt.Errorf("config applies to the local project; drop it when naming a script or --ns")
		}
		rs, err := buildRemoteStatus(ctx, req.Account, req.Script, req.NS, req.Objects, req.NoPing)
		if err != nil {
			return StatusReport{}, err
		}
		report = rs.report
	} else {
		ls, err := buildLocalStatus(ctx, req.Config, req.Objects, req.NoPing)
		if err != nil {
			return StatusReport{}, err
		}
		report = ls.report
	}
	return statusFromReport(report), nil
}

// statusFromReport maps an internal report into the typed StatusReport, routing
// any unknown key into Extra (stringified) so data is never lost.
func statusFromReport(m map[string]any) StatusReport {
	var st StatusReport
	b, _ := json.Marshal(m)
	_ = json.Unmarshal(b, &st)
	extra := map[string]string{}
	for k, v := range m {
		if !knownStatusKeys[k] {
			extra[k] = stringifyValue(v)
		}
	}
	if len(extra) > 0 {
		st.Extra = extra
	}
	return st
}

func stringifyValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// Settings returns the human-readable `cfdo init --show` text with credentials
// already masked. script, when set, adds that script's resolved secret line.
func (s *service) Settings(ctx context.Context, script string) (string, error) {
	settings, err := loadSettings()
	if err != nil {
		return "", err
	}
	var b strings.Builder
	writeSettings(&b, settings, script)
	return b.String(), nil
}

// BackupEntry is one row of the backups view, derived from a manifest.json.
type BackupEntry struct {
	Dir         string `json:"dir"`
	Script      string `json:"script"`
	Namespace   string `json:"namespace"`
	Class       string `json:"class"`
	CreatedAt   string `json:"created_at"`
	ObjectCount int    `json:"object_count"`
	Failures    int    `json:"failures"`
}

// Backups scans ./backups for backup directories, newest first. A missing
// directory is not an error — it just means no backups yet.
func (s *service) Backups(ctx context.Context) ([]BackupEntry, error) {
	entries, err := os.ReadDir("backups")
	if err != nil {
		if os.IsNotExist(err) {
			return []BackupEntry{}, nil
		}
		return nil, err
	}
	out := []BackupEntry{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join("backups", e.Name())
		raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
		if err != nil {
			continue // not a backup directory
		}
		var m Manifest
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		out = append(out, BackupEntry{
			Dir:         dir,
			Script:      m.Script,
			Namespace:   m.NamespaceID,
			Class:       m.Class,
			CreatedAt:   m.CreatedAt,
			ObjectCount: len(m.Objects),
			Failures:    len(m.Failed),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

// Plugins returns the vendored plugins recorded in the nearest cfdo.json.
// Outside a project it returns an empty list rather than an error.
func (s *service) Plugins(ctx context.Context) ([]PluginLock, error) {
	cfg, err := loadConfig("")
	if err != nil {
		return []PluginLock{}, nil
	}
	if cfg.Plugins == nil {
		return []PluginLock{}, nil
	}
	return cfg.Plugins, nil
}

// ---- actions + jobs ----------------------------------------------------

// EventKind distinguishes a log line, a progress tick, and the terminal event.
type EventKind string

const (
	EventLog      EventKind = "log"
	EventProgress EventKind = "progress"
	EventDone     EventKind = "done"
)

// Event is one unit of an action's live output:
//   - Kind "log":      a masked output line in Text, tagged Stream (stdout/stderr).
//   - Kind "progress": the backup/restore meter — Done/Failed of Total objects
//     (Total==0 means "no count yet", render a spinner).
//   - Kind "done":     the single terminal event; Final=true and Err is the
//     outcome (nil = success). The channel closes right after.
type Event struct {
	Kind   EventKind `json:"kind"`
	Stream string    `json:"stream,omitempty"`
	Text   string    `json:"text,omitempty"`
	Done   int       `json:"done,omitempty"`
	Failed int       `json:"failed,omitempty"`
	Total  int       `json:"total,omitempty"`
	Final  bool      `json:"final,omitempty"`
	Err    error     `json:"-"`
}

// Job is a running action: range over Events until the channel closes (the last
// one has Final=true and the Err), and call Cancel to abort it.
type Job struct {
	Events <-chan Event
	Cancel func()
}

// actionMu serializes actions: runAction swaps the process-wide os.Stdout and
// os.Stderr to capture the command's output and installs a progress sink, so
// only one action may run at a time — which matches the single job console.
var actionMu sync.Mutex

// progressSink, when non-nil (set under actionMu for the duration of one
// action), receives per-object progress from backup/restore. It is nil for the
// CLI, which renders its own inline meter instead.
var progressSink func(done, failed, total int)

// emitProgress is called by long-running commands (backup, restore) to report
// per-object progress. It is a no-op unless a Service action installed a sink.
func emitProgress(done, failed, total int) {
	if progressSink != nil {
		progressSink(done, failed, total)
	}
}

// runAction runs cfdo's own command logic in-process (no HTTP, no subprocess)
// as a *Job. The job's goroutine streams every log line and progress tick to
// Events, then sends one terminal Event (Final=true, Err=<result>) and closes.
func (s *service) runAction(ctx context.Context, args ...string) *Job {
	cctx, cancel := context.WithCancel(ctx)
	ch := make(chan Event, 64)
	j := &Job{Events: ch, Cancel: cancel}

	go func() {
		defer cancel()
		actionMu.Lock()
		defer actionMu.Unlock()

		progressSink = func(done, failed, total int) {
			ch <- Event{Kind: EventProgress, Done: done, Failed: failed, Total: total}
		}
		defer func() { progressSink = nil }()

		origOut, origErr := os.Stdout, os.Stderr
		rOut, wOut, err := os.Pipe()
		if err != nil {
			ch <- Event{Kind: EventDone, Final: true, Err: err}
			close(ch)
			return
		}
		rErr, wErr, err := os.Pipe()
		if err != nil {
			wOut.Close()
			rOut.Close()
			ch <- Event{Kind: EventDone, Final: true, Err: err}
			close(ch)
			return
		}
		os.Stdout, os.Stderr = wOut, wErr

		var wg sync.WaitGroup
		wg.Add(2)
		go s.scan(&wg, rOut, "stdout", ch)
		go s.scan(&wg, rErr, "stderr", ch)

		runErr := execute(cctx, args...)

		os.Stdout, os.Stderr = origOut, origErr
		wOut.Close()
		wErr.Close()
		wg.Wait()
		rOut.Close()
		rErr.Close()

		ch <- Event{Kind: EventDone, Final: true, Err: runErr}
		close(ch)
	}()

	return j
}

func (s *service) scan(wg *sync.WaitGroup, r io.Reader, stream string, ch chan<- Event) {
	defer wg.Done()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		ch <- Event{Kind: EventLog, Stream: stream, Text: s.mask.line(sc.Text())}
	}
}

// Delete mirrors `cfdo delete`. It is synchronous: ConfirmName must equal
// Script (the view makes the user type the name), then it runs with --yes.
// DESTRUCTIVE.
func (s *service) Delete(ctx context.Context, req DeleteRequest) error {
	if req.Script == "" {
		return fmt.Errorf("script is required")
	}
	if req.ConfirmName != req.Script {
		return fmt.Errorf("confirm_name must equal the script name")
	}
	a := []string{"delete", req.Script, "--yes"} // confirmed above
	a = argStr(a, "--ns", req.NS)
	a = argStr(a, "--account", req.Account)
	return execute(ctx, a...)
}

// Backup mirrors `cfdo backup`.
func (s *service) Backup(ctx context.Context, req BackupRequest) (*Job, error) {
	a := []string{"backup"}
	a = argStr(a, "--config", req.Config)
	a = argStr(a, "--output", req.Output)
	a = argPosInt(a, "--concurrency", req.Concurrency)
	a = argPosInt(a, "--limit", req.Limit)
	a = argStr(a, "--ids-file", req.IDsFile)
	a = argFlag(a, "--include-empty", req.IncludeEmpty)
	a = argFlag(a, "--continue-on-error", req.ContinueOnError)
	return s.runAction(ctx, a...), nil
}

// Restore mirrors `cfdo restore`. Confirm must be true (replace mode is
// destructive); the view does its own typed confirmation before setting it.
func (s *service) Restore(ctx context.Context, req RestoreRequest) (*Job, error) {
	if req.Dir == "" {
		return nil, fmt.Errorf("dir is required")
	}
	if req.Mode != "" && req.Mode != "merge" && req.Mode != "replace" {
		return nil, fmt.Errorf("mode must be merge or replace")
	}
	if !req.Confirm {
		return nil, fmt.Errorf("confirm must be true")
	}
	a := []string{"restore", req.Dir, "--yes"} // no TTY; confirmed above
	a = argStr(a, "--config", req.Config)
	a = argStr(a, "--mode", req.Mode)
	a = argStr(a, "--only", req.Only)
	a = argPosInt(a, "--concurrency", req.Concurrency)
	a = argFlag(a, "--skip-verify", req.SkipVerify)
	return s.runAction(ctx, a...), nil
}

// Upload mirrors `cfdo upload`.
func (s *service) Upload(ctx context.Context, req UploadRequest) (*Job, error) {
	a := []string{"upload"}
	a = argStr(a, "--config", req.Config)
	a = argStr(a, "--tag", req.Tag)
	a = argFlag(a, "--dry-run", req.DryRun)
	a = argFlag(a, "--no-secret", req.NoSecret)
	a = argFlag(a, "--no-assets", req.NoAssets)
	a = argRepeat(a, "--new-class", req.NewClass)
	a = argRepeat(a, "--deleted-class", req.DeletedClass)
	a = argRepeat(a, "--renamed-class", req.RenamedClass)
	return s.runAction(ctx, a...), nil
}

// Create mirrors `cfdo create <name>`.
func (s *service) Create(ctx context.Context, req CreateRequest) (*Job, error) {
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	a := []string{"create", req.Name}
	a = argStr(a, "--dir", req.Dir)
	a = argStr(a, "--class", req.Class)
	a = argStr(a, "--binding", req.Binding)
	a = argStr(a, "--compat-date", req.CompatDate)
	a = argStr(a, "--account", req.Account)
	a = argFlag(a, "--kv", req.KV)
	a = argFlag(a, "--no-skill", req.NoSkill)
	a = argFlag(a, "--custom-secret", req.CustomSecret)
	a = argFlag(a, "--force", req.Force)
	a = argRepeat(a, "--plugin", req.Plugins)
	return s.runAction(ctx, a...), nil
}

// Plugin mirrors `cfdo plugin <action>`.
func (s *service) Plugin(ctx context.Context, req PluginRequest) (*Job, error) {
	a := []string{"plugin"}
	switch req.Action {
	case "add":
		if req.Source == "" {
			return nil, fmt.Errorf("source is required for plugin add")
		}
		a = append(a, "add", req.Source)
	case "update":
		a = append(a, "update")
		if req.Name != "" {
			a = append(a, req.Name)
		}
	case "list":
		a = append(a, "list")
	default:
		return nil, fmt.Errorf("action must be add, update or list")
	}
	return s.runAction(ctx, a...), nil
}

// ---- request types -----------------------------------------------------

// BackupRequest maps to `cfdo backup`.
type BackupRequest struct {
	Output          string
	Concurrency     int
	ContinueOnError bool
	Limit           int
	IDsFile         string
	IncludeEmpty    bool
	Config          string
}

// RestoreRequest maps to `cfdo restore`.
type RestoreRequest struct {
	Dir         string
	Mode        string // "merge" (default) or "replace"
	Only        string
	Concurrency int
	SkipVerify  bool
	Config      string
	Confirm     bool
}

// CreateRequest maps to `cfdo create <name>`.
type CreateRequest struct {
	Name         string
	Dir          string
	Class        string
	Binding      string
	CompatDate   string
	Account      string
	KV           bool
	NoSkill      bool
	CustomSecret bool
	Force        bool
	Plugins      []string
}

// UploadRequest maps to `cfdo upload`.
type UploadRequest struct {
	Config       string
	Tag          string
	DryRun       bool
	NoSecret     bool
	NoAssets     bool
	NewClass     []string
	DeletedClass []string
	RenamedClass []string // each "old=new"
}

// DeleteRequest maps to `cfdo delete`.
type DeleteRequest struct {
	Script      string
	ConfirmName string
	NS          string
	Account     string
}

// PluginRequest maps to `cfdo plugin <action>`.
type PluginRequest struct {
	Action string // "add" | "update" | "list"
	Source string // required for add
	Name   string // optional for update
}

// ---- arg helpers -------------------------------------------------------

func argStr(a []string, flag, v string) []string {
	if v != "" {
		return append(a, flag, v)
	}
	return a
}

func argPosInt(a []string, flag string, n int) []string {
	if n > 0 {
		return append(a, flag, strconv.Itoa(n))
	}
	return a
}

func argFlag(a []string, flag string, on bool) []string {
	if on {
		return append(a, flag)
	}
	return a
}

func argRepeat(a []string, flag string, vs []string) []string {
	for _, v := range vs {
		if v != "" {
			a = append(a, flag, v)
		}
	}
	return a
}

// ---- secret masking ----------------------------------------------------

// secretMasker redacts known credential values out of streamed output. The
// Service inherits the user's environment, so a secret can surface in a line
// (an error echoing a URL with a token, say); we scrub the values we know
// rather than trust every code path. Masking is required even though there is
// no network, because the output is shown in the app's job console.
type secretMasker struct {
	secrets []string
}

func newSecretMasker(values ...string) *secretMasker {
	m := &secretMasker{}
	for _, v := range values {
		if len(v) >= 6 { // ignore short/empty values so we never blank real text
			m.secrets = append(m.secrets, v)
		}
	}
	return m
}

func (m *secretMasker) line(s string) string {
	if m == nil {
		return s
	}
	for _, sec := range m.secrets {
		s = strings.ReplaceAll(s, sec, "***")
	}
	return s
}
