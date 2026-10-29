package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

	"github.com/funkyhq/cli/internal/auth"
	"github.com/funkyhq/cli/internal/funky"
)

var noBrowser bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Log in to Funky Cloud in your browser",
	Long: `Log in to Funky Cloud as yourself. This opens your browser, where you sign
in, pick the project the CLI should work in, and approve this machine. Run it
again to switch projects.

The login lasts 90 days and can be revoked from Settings in the web app or
with ` + "`funky logout`" + `. On a machine without a browser, use --no-browser and
paste the code the page shows.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ep := auth.EndpointsFromEnv()
		out := cmd.OutOrStdout()
		device, _ := os.Hostname()
		var credentialInFile bool
		session, err := auth.Login(cmd.Context(), auth.LoginOptions{
			Endpoints:  ep,
			DeviceName: device,
			NoBrowser:  noBrowser,
			Save: func(s *auth.Session) error {
				// Replace, don't stack: the previous login is revoked, but only
				// once the new one is safely saved — a failed save must leave
				// the user logged in as before.
				old, _ := auth.Load()
				inFile, err := auth.Save(s)
				if err != nil {
					return err
				}
				credentialInFile = inFile
				if old != nil {
					_ = auth.NewConsole(old.Console).Logout(cmd.Context(), old.Credential)
				}
				return nil
			},
			In:  cmd.InOrStdin(),
			Out: out,
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "\nLogged in as %s · %s\n", session.User.Email, session.Target())
		if credentialInFile {
			fmt.Fprintln(out, "No OS keychain was available, so the credential is saved in a file only you can read.")
		}
		if apiKey() != "" {
			fmt.Fprintln(out, "Note: FUNKY_API_KEY is set, and takes precedence over this login.")
		}
		return nil
	},
}

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log this machine out of Funky Cloud",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		session, err := auth.Load()
		if errors.Is(err, auth.ErrNotLoggedIn) {
			fmt.Fprintln(out, "Not logged in.")
			return auth.Delete(nil)
		}
		if err != nil {
			// e.g. a locked keychain: keep everything, so a retry can still revoke.
			return err
		}
		// Best effort: an expired or already-revoked login (401) is logged out anyway.
		err = auth.NewConsole(session.Console).Logout(cmd.Context(), session.Credential)
		var apiErr *funky.APIError
		if err != nil && !(errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized) {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: couldn't revoke the login on the server: %v\n", err)
		}
		if err := auth.Delete(session); err != nil {
			return err
		}
		fmt.Fprintf(out, "Logged out %s.\n", session.User.Email)
		return nil
	},
}

var whoamiCmd = &cobra.Command{
	Use:   "whoami",
	Short: "Show who the CLI is acting as, and in which project",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		out := cmd.OutOrStdout()
		if apiKey() != "" {
			fmt.Fprintln(out, "Using FUNKY_API_KEY: acting as the API key, in its project.")
			return nil
		}
		ep := auth.EndpointsFromEnv()
		session, err := loadSession(ep)
		if err != nil {
			return err
		}
		// Asks the server, so a revoked login or lost access shows up here.
		access, err := auth.NewConsole(ep.Console).AccessToken(cmd.Context(), session.Credential)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Logged in as %s\nProject: %s\n", access.User.Email, access.Target())
		return nil
	},
}

func init() {
	loginCmd.Flags().BoolVar(&noBrowser, "no-browser", false, "print the login link and paste the code instead of opening a browser")
	rootCmd.AddCommand(loginCmd, logoutCmd, whoamiCmd)
}
