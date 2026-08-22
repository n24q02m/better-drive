package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/n24q02m/better-drive/internal/exitcode"
	"github.com/n24q02m/better-drive/internal/output"
	"github.com/n24q02m/better-drive/internal/restore"
	"github.com/spf13/cobra"
)

func restoreCmd() *cobra.Command {
	c := &cobra.Command{
		Use:     "restore",
		Short:   "Plan safe isolated restores",
		Long:    "Restore planning is read-only. Live fetch/apply remains separately gated and is not performed by this command group yet.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
	}
	c.AddCommand(restorePlanCmd())
	return c
}

func restorePlanCmd() *cobra.Command {
	var root string
	var manifest string
	var format string
	c := &cobra.Command{
		Use:     "plan",
		Short:   "Validate a restore manifest without writing",
		Long:    "Validate canonical relative paths, duplicate destinations, and existing conflicts without touching the restore root.",
		Example: "  better-drive restore plan --root C:/staging --manifest restore.json --format json",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := output.Validate(format); err != nil {
				return badFormatErr(err)
			}
			if root == "" || manifest == "" {
				return exitcode.WithRemediation(exitcode.ConfigError(errors.New("restore plan requires --root and --manifest")), "set an isolated absolute --root and a JSON --manifest")
			}
			file, err := os.Open(manifest)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), fmt.Sprintf("read restore manifest %s", manifest))
			}
			defer file.Close()
			var entries []restore.Entry
			if err := json.NewDecoder(file).Decode(&entries); err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "manifest must be a JSON array of restore entries")
			}
			plan, err := restore.BuildPlan(root, entries)
			if err != nil {
				return exitcode.WithRemediation(exitcode.ConfigError(err), "fix unsafe or duplicate restore paths before fetch")
			}
			if format == output.FormatJSON {
				return output.RenderJSON(cmd.OutOrStdout(), plan)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "restore root: %s\nentries: %d\nconflicts: %d\n", plan.Root, len(plan.Entries), len(plan.Conflicts))
			return nil
		},
	}
	output.AddFormatFlag(c, &format)
	c.Flags().StringVar(&root, "root", "", "isolated absolute restore root")
	c.Flags().StringVar(&manifest, "manifest", "", "JSON restore manifest")
	return c
}
