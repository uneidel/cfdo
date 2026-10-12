//go:build withgui

package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// TestWithGUI_HelpListsUICommand is the withgui link+wiring smoke. It only compiles under
// `-tags withgui` (and CGO + the GL toolchain), so it never runs in the default headless CI
// — that build has no `ui` command and no Fyne link. Its presence here proves two things the
// headless ui/ tests cannot: that the GUI build links at all, and that `cfdo --help` actually
// advertises the `ui` subcommand to the user. `--help` returns before any window is opened,
// so no display is needed to run it.
func TestWithGUI_HelpListsUICommand(t *testing.T) {
	out := captureStdout(t, func() {
		if err := run(context.Background(), "--help"); err != nil {
			t.Fatalf("run(--help) = %v, want nil", err)
		}
	})
	if !strings.Contains(out, "ui") {
		t.Errorf("--help output does not list the `ui` command:\n%s", out)
	}
}

// captureStdout swaps os.Stdout for a pipe for the duration of fn (cobra writes help to
// os.Stdout by default) and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	fn()

	w.Close()
	os.Stdout = orig
	return <-done
}
