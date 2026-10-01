package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
)

// Manifest describes a backup directory. Written last, so its presence means
// the backup completed.
type Manifest struct {
	Format      int          `json:"format"`
	CreatedAt   string       `json:"created_at"`
	AccountID   string       `json:"account_id"`
	Script      string       `json:"script"`
	Class       string       `json:"class"`
	Binding     string       `json:"binding"`
	NamespaceID string       `json:"namespace_id"`
	SQLite      bool         `json:"sqlite"`
	Objects     []ObjectMeta `json:"objects"`
	Failed      []ObjectFail `json:"failed,omitempty"`
}

type ObjectMeta struct {
	ID     string `json:"id"`
	Name   string `json:"name,omitempty"`
	File   string `json:"file"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type ObjectFail struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

func newBackupCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Dump every object's storage to a local directory",
		Long:  "Exports every object's storage through the worker's admin route.",
		Args:  cobra.NoArgs,
	}
	fs := cmd.Flags()
	confPath := fs.StringP("config", "c", "", "path to cfdo.json (default: nearest one up the tree)")
	out := fs.StringP("output", "o", "", "output directory (default: ./backups/<script>-<timestamp>)")
	concurrency := fs.Int("concurrency", 8, "objects to export in parallel")
	limit := fs.Int("limit", 0, "stop after this many objects (0 = all)")
	includeEmpty := fs.Bool("include-empty", false, "also export objects Cloudflare reports as having no stored data")
	idsFile := fs.String("ids-file", "", "file of extra object ids to export, one per line (for objects created with newUniqueId)")
	continueOnError := fs.Bool("continue-on-error", false, "record failures in the manifest instead of aborting")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		if *concurrency < 1 {
			return fmt.Errorf("--concurrency must be at least 1")
		}

		cfg, err := loadConfig(*confPath)
		if err != nil {
			return err
		}
		token, err := cfg.token()
		if err != nil {
			return err
		}
		secret, err := cfg.secret()
		if err != nil {
			return err
		}

		client := NewClient(token)
		ns, err := client.FindNamespace(ctx, cfg.AccountID, cfg.ScriptName, cfg.ClassName)
		if err != nil {
			return err
		}

		sub, _ := client.WorkersDevSubdomain(ctx, cfg.AccountID)
		workerURL, err := cfg.resolveWorkerURL(sub)
		if err != nil {
			return err
		}
		admin := newAdminClient(workerURL, secret)
		if _, err := admin.Ping(ctx); err != nil {
			return fmt.Errorf("admin route check failed: %w", err)
		}

		objects, sources, err := discoverObjects(ctx, client, admin, cfg, ns, *includeEmpty, *idsFile)
		if err != nil {
			return err
		}
		if *limit > 0 && len(objects) > *limit {
			objects = objects[:*limit]
		}
		if len(objects) == 0 {
			return fmt.Errorf("found no objects to back up in namespace %s\n  (%s)\n  For objects created with newUniqueId(), pass their ids with --ids-file.", ns.ID, sources)
		}

		dir := *out
		if dir == "" {
			dir = filepath.Join(cfg.dir, "backups", fmt.Sprintf("%s-%s", cfg.ScriptName, time.Now().UTC().Format("20060102T150405Z")))
		}
		objDir := filepath.Join(dir, "objects")
		if err := os.MkdirAll(objDir, 0o755); err != nil {
			return err
		}

		fmt.Printf("Backing up %d object(s) from %s/%s -> %s\n", len(objects), cfg.ScriptName, cfg.ClassName, dir)
		fmt.Printf("  discovery: %s\n", sources)

		var (
			mu       sync.Mutex
			metas    []ObjectMeta
			failures []ObjectFail
			done     atomic.Int64
			bytes    atomic.Int64
		)

		cctx, cancel := context.WithCancel(ctx)
		defer cancel()

		sem := make(chan struct{}, *concurrency)
		var wg sync.WaitGroup
		var firstErr error
		var errOnce sync.Once

		for _, obj := range objects {
			select {
			case sem <- struct{}{}:
			case <-cctx.Done():
			}
			if cctx.Err() != nil {
				break
			}
			wg.Add(1)
			go func(obj namedObject) {
				defer wg.Done()
				defer func() { <-sem }()

				data, err := admin.Export(cctx, obj.ID)
				if err != nil {
					if *continueOnError {
						mu.Lock()
						failures = append(failures, ObjectFail{ID: obj.ID, Error: err.Error()})
						mu.Unlock()
						fmt.Fprintf(os.Stderr, "  ! %s: %v\n", shorten(obj.ID, 16), err)
						return
					}
					errOnce.Do(func() { firstErr = fmt.Errorf("exporting %s: %w", obj.ID, err); cancel() })
					return
				}
				name := filepath.Join("objects", obj.ID+".json")
				if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
					errOnce.Do(func() { firstErr = err; cancel() })
					return
				}
				sum := sha256.Sum256(data)
				mu.Lock()
				metas = append(metas, ObjectMeta{ID: obj.ID, Name: obj.Name, File: name, Bytes: len(data), SHA256: hex.EncodeToString(sum[:])})
				mu.Unlock()

				bytes.Add(int64(len(data)))
				if n := done.Add(1); n%10 == 0 || int(n) == len(objects) {
					fmt.Printf("\r  %d/%d objects, %s", n, len(objects), humanBytes(bytes.Load()))
				}
			}(obj)
		}
		wg.Wait()
		fmt.Println()

		if firstErr != nil {
			return fmt.Errorf("%w\n  (partial output left in %s; pass --continue-on-error to skip failures)", firstErr, dir)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		sortMetas(metas)
		man := Manifest{
			Format:      1,
			CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			AccountID:   cfg.AccountID,
			Script:      cfg.ScriptName,
			Class:       cfg.ClassName,
			Binding:     cfg.Binding,
			NamespaceID: ns.ID,
			SQLite:      ns.UseSQLite,
			Objects:     metas,
			Failed:      failures,
		}
		b, err := json.MarshalIndent(man, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o600); err != nil {
			return err
		}

		fmt.Printf("Backup complete: %d object(s), %s, in %s\n", len(metas), humanBytes(bytes.Load()), dir)
		if len(failures) > 0 {
			fmt.Fprintf(os.Stderr, "%d object(s) failed and are listed in manifest.json\n", len(failures))
		}
		return nil
	}
	return cmd
}

// namedObject is a discovery result: an id, plus the routing name when the
// worker's index knew one.
type namedObject struct {
	ID   string
	Name string
}

// discoverObjects unions every source that can name an object. For
// SQLite-backed namespaces the Cloudflare listing API is lagging and
// incomplete — freshly written objects can be missing from it for a long time
// — so the worker's own index is what actually finds them.
func discoverObjects(ctx context.Context, client *Client, admin *adminClient, cfg *Config, ns *Namespace, includeEmpty bool, idsFile string) ([]namedObject, string, error) {
	seen := map[string]*namedObject{}
	order := []string{}
	add := func(id, name string) {
		if id == "" {
			return
		}
		if existing, ok := seen[id]; ok {
			if existing.Name == "" {
				existing.Name = name
			}
			return
		}
		seen[id] = &namedObject{ID: id, Name: name}
		order = append(order, id)
	}

	var notes []string

	apiObjs, apiErr := client.ListObjects(ctx, cfg.AccountID, ns.ID)
	switch {
	case apiErr != nil:
		notes = append(notes, "API listing failed: "+apiErr.Error())
	default:
		n := 0
		for _, o := range apiObjs {
			if !includeEmpty && !o.HasStoredData {
				continue
			}
			add(o.ID, "")
			n++
		}
		notes = append(notes, fmt.Sprintf("API listing: %d", n))
		if ns.UseSQLite {
			notes[len(notes)-1] += " (incomplete for SQLite-backed namespaces)"
		}
	}

	idxObjs, idxErr := admin.List(ctx)
	if idxErr != nil {
		notes = append(notes, "worker index unavailable: "+idxErr.Error())
	} else {
		for _, e := range idxObjs {
			add(e.ID, e.Name)
		}
		notes = append(notes, fmt.Sprintf("worker index: %d", len(idxObjs)))
	}

	if idsFile != "" {
		raw, err := os.ReadFile(idsFile)
		if err != nil {
			return nil, "", fmt.Errorf("reading --ids-file: %w", err)
		}
		n := 0
		for _, line := range strings.Split(string(raw), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			add(line, "")
			n++
		}
		notes = append(notes, fmt.Sprintf("ids-file: %d", n))
	}

	if apiErr != nil && idxErr != nil {
		return nil, "", fmt.Errorf("no way to discover objects — API listing and worker index both failed:\n  %v\n  %v", apiErr, idxErr)
	}

	out := make([]namedObject, 0, len(order))
	for _, id := range order {
		out = append(out, *seen[id])
	}
	return out, strings.Join(notes, "; "), nil
}

func sortMetas(m []ObjectMeta) {
	for i := 1; i < len(m); i++ {
		for j := i; j > 0 && m[j].ID < m[j-1].ID; j-- {
			m[j], m[j-1] = m[j-1], m[j]
		}
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
