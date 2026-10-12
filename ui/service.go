package ui

import (
	"context"

	"github.com/ulrich/cfdo/internal/core"
)

// Service is the view's own narrow seam onto core. It is declared HERE (not imported
// from core) so backend-dev's concrete *service structurally satisfies it with no change
// in core, and tests can inject a fake (see fake_test.go). The method set mirrors the
// ratified core.Service (#114) — the window builds one at ui-4 via ui.NewCommand(core.NewService()).
type Service interface {
	Namespaces(ctx context.Context, r core.NamespacesRequest) ([]core.NamespaceReport, error)
	Status(ctx context.Context, r core.StatusRequest) (core.StatusReport, error)
	Settings(ctx context.Context, script string) (string, error)
	Backups(ctx context.Context) ([]core.BackupEntry, error)
	Plugins(ctx context.Context) ([]core.PluginLock, error)

	Delete(ctx context.Context, r core.DeleteRequest) error // synchronous; no Job

	Backup(ctx context.Context, r core.BackupRequest) (*core.Job, error)
	Restore(ctx context.Context, r core.RestoreRequest) (*core.Job, error)
	Upload(ctx context.Context, r core.UploadRequest) (*core.Job, error)
	Create(ctx context.Context, r core.CreateRequest) (*core.Job, error)
	Plugin(ctx context.Context, r core.PluginRequest) (*core.Job, error)
}

// Compile-time assertion that the ratified core.Service satisfies the view's narrow seam.
// (When the real core lands, this guards against any method-set drift at the handoff.)
var _ Service = (core.Service)(nil)
