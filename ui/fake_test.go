package ui

import (
	"context"

	"github.com/ulrich/cfdo/internal/core"
)

// fakeService is an in-memory Service for headless tests — it injects canned read results and
// replays a scripted Event stream for the streaming actions. It proves ui.Service is satisfiable
// without backend-dev's real core and lets the console reducer be driven end-to-end.
type fakeService struct {
	namespaces []core.NamespaceReport
	status     core.StatusReport
	settings   string
	backups    []core.BackupEntry
	plugins    []core.PluginLock

	readErr   error // returned by the read methods when set
	deleteErr error // returned by Delete

	jobScript []core.Event // events every streaming action replays
	jobErr    error        // validation error: returned instead of a *Job (nothing starts)

	cancelled bool // set when a returned Job's Cancel is invoked
}

func (f *fakeService) Namespaces(_ context.Context, _ core.NamespacesRequest) ([]core.NamespaceReport, error) {
	return f.namespaces, f.readErr
}
func (f *fakeService) Status(_ context.Context, _ core.StatusRequest) (core.StatusReport, error) {
	return f.status, f.readErr
}
func (f *fakeService) Settings(_ context.Context, _ string) (string, error) {
	return f.settings, f.readErr
}
func (f *fakeService) Backups(_ context.Context) ([]core.BackupEntry, error) {
	return f.backups, f.readErr
}
func (f *fakeService) Plugins(_ context.Context) ([]core.PluginLock, error) {
	return f.plugins, f.readErr
}
func (f *fakeService) Delete(_ context.Context, _ core.DeleteRequest) error { return f.deleteErr }

func (f *fakeService) Backup(_ context.Context, _ core.BackupRequest) (*core.Job, error) {
	return f.job()
}
func (f *fakeService) Restore(_ context.Context, _ core.RestoreRequest) (*core.Job, error) {
	return f.job()
}
func (f *fakeService) Upload(_ context.Context, _ core.UploadRequest) (*core.Job, error) {
	return f.job()
}
func (f *fakeService) Create(_ context.Context, _ core.CreateRequest) (*core.Job, error) {
	return f.job()
}
func (f *fakeService) Plugin(_ context.Context, _ core.PluginRequest) (*core.Job, error) {
	return f.job()
}

// job returns either a validation error (nothing started) or a running Job replaying jobScript.
func (f *fakeService) job() (*core.Job, error) {
	if f.jobErr != nil {
		return nil, f.jobErr
	}
	ch := make(chan core.Event, len(f.jobScript))
	for _, e := range f.jobScript {
		ch <- e
	}
	close(ch)
	return &core.Job{Events: ch, Cancel: func() { f.cancelled = true }}, nil
}

// compile-time: the fake satisfies the view's narrow seam.
var _ Service = (*fakeService)(nil)
