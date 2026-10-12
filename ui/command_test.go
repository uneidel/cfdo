package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/ulrich/cfdo/internal/core"
)

// The fake satisfies the narrow ui.Service; this asserts it also satisfies the
// authoritative core.Service, which is exactly what NewCommand accepts. If the two
// method sets ever drift at the real-core handoff, this stops compiling here.
var _ core.Service = (*fakeService)(nil)

// NewCommand must stand up a well-formed, no-arg `ui` subcommand regardless of
// build tag — this is the CGO-off seam the withgui link contract rests on.
func TestNewCommandShape(t *testing.T) {
	cmd := NewCommand(&fakeService{})
	if cmd == nil {
		t.Fatal("NewCommand returned nil")
	}
	if cmd.Use != "ui" {
		t.Errorf("Use = %q, want %q", cmd.Use, "ui")
	}
	if cmd.RunE == nil {
		t.Error("RunE is nil — the command would do nothing")
	}
	if err := cmd.Args(cmd, []string{"unexpected"}); err == nil {
		t.Error("expected NoArgs to reject a positional argument")
	}
}

// TestNewCommandMetadata pins the user-facing command metadata: a help line and a
// long description that steers the user toward the GUI build, and SilenceUsage so a
// runtime failure (e.g. the no-GUI build error) is not buried under a usage dump.
func TestNewCommandMetadata(t *testing.T) {
	cmd := NewCommand(&fakeService{})
	if cmd.Short == "" {
		t.Error("Short help is empty")
	}
	if !strings.Contains(cmd.Long, "withgui") {
		t.Errorf("Long should mention the withgui build requirement, got %q", cmd.Long)
	}
	if !cmd.SilenceUsage {
		t.Error("SilenceUsage should be set so a RunE error isn't drowned in usage output")
	}
}

// In the default (!withgui) build, executing the command must reach Run and come
// back with the explicit rebuild instruction — proving RunE→Run is wired, not just
// that it compiles. Under -tags withgui the same path opens the Fyne window instead.
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
