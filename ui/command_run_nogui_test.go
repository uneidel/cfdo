//go:build !withgui

package ui

import (
	"context"
	"strings"
	"testing"
)

// In the default (!withgui) build, executing the command must reach Run and come
// back with the explicit rebuild instruction — proving RunE→Run is wired, not just
// that it compiles.
//
// This test is guarded to the no-GUI build on purpose (ui-9). Under -tags withgui
// the same RunE path calls the real app.New()/w.ShowAndRun() in run_gui.go, which
// opens a native Fyne window and blocks forever — so running this test there hangs
// the whole `go test -tags withgui ./ui/` suite. The assertion below ("expected an
// error from the no-GUI build") is only meaningful without the tag anyway, so the
// test belongs exclusively to the !withgui build.
func TestNewCommandRunWithoutGUI(t *testing.T) {
	cmd := NewCommand(&fakeService{})
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(context.Background())
	if err == nil {
		t.Fatal("expected an error from the no-GUI build, got nil")
	}
	if !strings.Contains(err.Error(), "withgui") {
		t.Errorf("error %q should tell the user to rebuild with -tags withgui", err)
	}
}
