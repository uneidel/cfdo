package ui

import (
	"github.com/spf13/cobra"

	"github.com/ulrich/cfdo/internal/core"
)

// NewCommand builds the `ui` subcommand: the single, build-tag-free entry point
// that wires the native desktop view onto an already-constructed core.Service.
//
// This is the entire ui↔cmd link contract (squad ui-3). It is ALWAYS compiled —
// CGO-off, no Fyne, no GL — so `go build ./...` stays green on hosts without the
// display toolchain. The window itself lives behind the `withgui` build tag: RunE
// calls Run, which is the real Fyne bootstrap under `-tags withgui` (run_gui.go)
// and a short "rebuild with -tags withgui" error otherwise (run_stub.go).
//
// core.Service structurally satisfies the view's narrow ui.Service seam (asserted
// in service.go), so backend-dev's concrete *service flows straight through here
// into Run with no adapter.
func NewCommand(svc core.Service) *cobra.Command {
	return &cobra.Command{
		Use:   "ui",
		Short: "Open the cfdo desktop dashboard",
		Long: "Open the native cfdo dashboard: namespaces with object counts, per-script\n" +
			"status cards, and backup/restore with live logs — a render-only view over the\n" +
			"same core operations the CLI uses. Requires a GUI build (-tags withgui).",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Run(cmd.Context(), svc)
		},
	}
}
