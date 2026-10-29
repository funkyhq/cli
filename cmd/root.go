package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/funkyhq/cli/internal/auth"
	"github.com/funkyhq/cli/internal/funky"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "funky",
	Short: "Manage Funky agents as code",
	Long: `funky manages Funky agent configs from YAML files, like Terraform manages
infrastructure: describe each agent in agents/<name>.yaml, then run
` + "`funky apply`" + ` to create or update them in Funky.

  funky login    log in to Funky Cloud in your browser
  funky init     create the project structure (an agents/ folder)
  funky apply    create or update the agent configs in agents/

In CI, set FUNKY_API_KEY to a key from https://platform.funky.dev/api-keys
instead of logging in; it takes precedence over a login.`,
	SilenceUsage: true,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func apiKey() string { return strings.TrimSpace(os.Getenv("FUNKY_API_KEY")) }

// newClient builds an API client from FUNKY_API_KEY or, failing that, the
// saved login. target names the project it acts on, for the plan header.
func newClient() (client *funky.Client, target string, err error) {
	ep := auth.EndpointsFromEnv()
	suffix := ""
	if ep.API != auth.DefaultAPIURL {
		suffix = " at " + ep.API
	}
	if key := apiKey(); key != "" {
		return funky.NewClient(ep.API, funky.StaticToken(key)), "the FUNKY_API_KEY project" + suffix, nil
	}
	session, err := loadSession(ep)
	if err != nil {
		return nil, "", err
	}
	tokens := &auth.TokenSource{Console: auth.NewConsole(ep.Console), Credential: session.Credential}
	return funky.NewClient(ep.API, tokens), session.Target() + suffix, nil
}

// loadSession is the saved login for ep's console, or a hint to log in.
func loadSession(ep auth.Endpoints) (*auth.Session, error) {
	session, err := auth.Load()
	if errors.Is(err, auth.ErrNotLoggedIn) {
		return nil, fmt.Errorf("%w: run `funky login` (or set FUNKY_API_KEY)", err)
	}
	if err != nil {
		return nil, err
	}
	if session.Console != ep.Console {
		return nil, fmt.Errorf("logged in to %s, not %s: run `funky login`", session.Console, ep.Console)
	}
	return session, nil
}
