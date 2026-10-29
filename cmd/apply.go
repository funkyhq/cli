package cmd

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/funkyhq/cli/internal/apply"
	"github.com/funkyhq/cli/internal/project"
)

var (
	autoApprove bool
	dryRun      bool
)

var applyCmd = &cobra.Command{
	Use:   "apply",
	Short: "Create or update the agent configs in agents/",
	Long: `Read every agents/*.yaml file and make Funky match it.

Each file is one agent config, named by its file name (or a name: field).
The name is stored in the config's metadata under "` + project.NameKey + `",
which is how later applies find the config to update. A config whose file
is unchanged is left alone; a changed one gets a new version with only the
changed fields. Removing a file does not archive its config.

apply shows the plan and asks for confirmation before changing anything.`,
	Example: `  # agents/researcher.yaml
  inference:
    provider: anthropic
    model: claude-sonnet-5
    maxTokens: 2048
  systemPrompt: You are an autonomous agent in a fresh Linux sandbox.
  network:
    type: unrestricted
  packages:
    pip: [pandas]`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		agents, err := project.LoadAgents(".")
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(agents) == 0 {
			fmt.Fprintf(out, "No agent files in %s/; nothing to apply.\n", project.AgentsDir)
			return nil
		}
		client, target, err := newClient()
		if err != nil {
			return err
		}
		ctx := cmd.Context()

		changes, err := apply.Plan(ctx, client, agents)
		if err != nil {
			return err
		}
		printPlan(out, target, changes)

		pending := 0
		for _, ch := range changes {
			if ch.Action != apply.Unchanged {
				pending++
			}
		}
		if pending == 0 {
			fmt.Fprintln(out, "\nNo changes. Funky matches your agent files.")
			return nil
		}
		if dryRun {
			return nil
		}
		if !autoApprove && !confirm(cmd.InOrStdin(), out) {
			return fmt.Errorf("apply cancelled")
		}

		results, err := apply.Apply(ctx, client, changes)
		fmt.Fprintln(out)
		printResults(out, results)
		if err != nil {
			return err
		}
		created, updated := 0, 0
		for _, r := range results {
			switch r.Change.Action {
			case apply.Create:
				created++
			case apply.Update:
				updated++
			}
		}
		fmt.Fprintf(out, "\nApply complete: %d created, %d updated, %d unchanged.\n",
			created, updated, len(results)-created-updated)
		return nil
	},
}

func init() {
	applyCmd.Flags().BoolVar(&autoApprove, "auto-approve", false, "apply without asking for confirmation")
	applyCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show the plan without applying it")
	rootCmd.AddCommand(applyCmd)
}

var symbols = map[apply.Action]string{apply.Create: "+", apply.Update: "~", apply.Unchanged: " "}

func printPlan(out io.Writer, target string, changes []apply.Change) {
	creates, updates := 0, 0
	fmt.Fprintf(out, "Plan for %s:\n\n", target)
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, ch := range changes {
		detail := ""
		switch ch.Action {
		case apply.Create:
			creates++
			detail = ch.Agent.File
		case apply.Update:
			updates++
			detail = fmt.Sprintf("%s v%d: %s", ch.Remote.ID, ch.Remote.Version, strings.Join(ch.Fields, ", "))
		case apply.Unchanged:
			detail = fmt.Sprintf("%s v%d", ch.Remote.ID, ch.Remote.Version)
		}
		fmt.Fprintf(w, "  %s %s\t%s\t%s\n", symbols[ch.Action], ch.Agent.Name, ch.Action, detail)
	}
	w.Flush()
	fmt.Fprintf(out, "\nPlan: %d to create, %d to update, %d unchanged.\n",
		creates, updates, len(changes)-creates-updates)
}

func printResults(out io.Writer, results []apply.Result) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range results {
		if r.Change.Action == apply.Unchanged {
			continue
		}
		fmt.Fprintf(w, "  %s %s\t%sd\t%s v%d\n",
			symbols[r.Change.Action], r.Change.Agent.Name, r.Change.Action, r.Config.ID, r.Config.Version)
	}
	w.Flush()
}

func confirm(in io.Reader, out io.Writer) bool {
	fmt.Fprint(out, "\nApply these changes? Only 'yes' will be accepted: ")
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil {
		fmt.Fprintln(out) // no answer typed: end the prompt's line
	}
	return strings.TrimSpace(line) == "yes"
}
