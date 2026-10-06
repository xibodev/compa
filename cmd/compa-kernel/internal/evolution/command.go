// Package evolution holds the `compa-kernel evolution` commands: the human
// review step for the skill drafts evolution writes.
package evolution

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/xibodev/compa/v2/cmd/compa-kernel/internal"
	"github.com/xibodev/compa/v2/pkg/evolution"
)

// target is the workspace and evolution state the commands work on.
type target struct {
	workspace string
	paths     evolution.Paths
	// write is whether an accepted draft is written at once: only in the
	// "apply" mode.
	write bool
}

type loadTarget func() (target, error)

func defaultTarget() (target, error) {
	cfg, err := internal.LoadConfig()
	if err != nil {
		return target{}, fmt.Errorf("error loading config: %w", err)
	}
	workspace := cfg.WorkspacePath()
	return target{
		workspace: workspace,
		paths:     evolution.NewPaths(workspace, cfg.Evolution.StateDir),
		write:     cfg.Evolution.AutoAppliesDrafts(),
	}, nil
}

func NewEvolutionCommand() *cobra.Command {
	return newEvolutionCommand(defaultTarget)
}

func newEvolutionCommand(load loadTarget) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "evolution",
		Short: "Review the skill drafts evolution proposes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	drafts := &cobra.Command{
		Use:   "drafts",
		Short: "List, accept or reject skill drafts",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	drafts.AddCommand(newListCommand(load), newAcceptCommand(load), newRejectCommand(load))
	cmd.AddCommand(drafts)
	return cmd
}

func newListCommand(load loadTarget) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List drafts waiting for review, with the change each one makes",
		Args:    cobra.NoArgs,
		Example: "compa-kernel evolution drafts list\ncompa-kernel evolution drafts list --all",
		RunE: func(cmd *cobra.Command, _ []string) error {
			t, err := load()
			if err != nil {
				return err
			}
			return listDrafts(cmd.OutOrStdout(), t, all)
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "Also list accepted, rejected and quarantined drafts")
	return cmd
}

func newAcceptCommand(load loadTarget) *cobra.Command {
	return &cobra.Command{
		Use:     "accept <draft-id>",
		Short:   "Accept a draft; in apply mode it is written to its skill at once (the old version is backed up)",
		Args:    cobra.ExactArgs(1),
		Example: "compa-kernel evolution drafts accept <draft-id>",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := load()
			if err != nil {
				return err
			}
			draft, err := evolution.AcceptDraft(context.Background(), t.paths, t.workspace, args[0], t.write, nil)
			if err != nil {
				return err
			}
			if draft.Status == evolution.DraftStatusApproved {
				fmt.Fprintf(cmd.OutOrStdout(), "Draft %s accepted; it is written to skill %q when evolution runs in apply mode.\n",
					draft.ID, draft.TargetSkillName)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Draft %s accepted: skill %q updated (%s).\n",
				draft.ID, draft.TargetSkillName, draft.ChangeKind)
			return nil
		},
	}
}

func newRejectCommand(load loadTarget) *cobra.Command {
	return &cobra.Command{
		Use:     "reject <draft-id>",
		Short:   "Reject a draft; it is never written",
		Args:    cobra.ExactArgs(1),
		Example: "compa-kernel evolution drafts reject <draft-id>",
		RunE: func(cmd *cobra.Command, args []string) error {
			t, err := load()
			if err != nil {
				return err
			}
			draft, err := evolution.RejectDraft(t.paths, t.workspace, args[0], nil)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Draft %s rejected.\n", draft.ID)
			return nil
		},
	}
}

func listDrafts(w io.Writer, t target, all bool) error {
	drafts, err := evolution.NewStore(t.paths).LoadDrafts()
	if err != nil {
		return err
	}
	shown := 0
	for _, draft := range drafts {
		if draft.WorkspaceID != t.workspace {
			continue
		}
		pending := draft.Status == evolution.DraftStatusCandidate || draft.Status == evolution.DraftStatusApproved
		if !pending && !all {
			continue
		}
		shown++
		fmt.Fprintf(w, "%s  [%s]  %s %q\n", draft.ID, draft.Status, draft.ChangeKind, draft.TargetSkillName)
		if s := strings.TrimSpace(draft.HumanSummary); s != "" {
			fmt.Fprintf(w, "  %s\n", s)
		}
		for _, finding := range draft.ScanFindings {
			fmt.Fprintf(w, "  finding: %s\n", finding)
		}
		if !pending {
			continue
		}
		preview, err := evolution.BuildDraftPreview(t.workspace, draft)
		if err != nil {
			fmt.Fprintf(w, "  preview unavailable: %v\n", err)
			continue
		}
		for _, line := range strings.Split(strings.TrimRight(preview.DiffPreview, "\n"), "\n") {
			fmt.Fprintf(w, "  %s\n", line)
		}
		fmt.Fprintln(w)
	}
	if shown == 0 {
		if all {
			fmt.Fprintln(w, "No drafts.")
		} else {
			fmt.Fprintln(w, "No drafts waiting for review.")
		}
	}
	return nil
}
