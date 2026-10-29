package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/funkyhq/cli/internal/project"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create the project structure in the current directory",
	Long: `Create the project structure in the current directory: an agents/ folder
for agent config files. Existing files are left untouched.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		created, err := project.Init(".")
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if !created {
			fmt.Fprintf(out, "%s/ already exists; nothing to do.\n", project.AgentsDir)
			return nil
		}
		fmt.Fprintf(out, "Created %s/. Add an agent config as %s/<name>.yaml, then run `funky apply`.\n",
			project.AgentsDir, project.AgentsDir)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
