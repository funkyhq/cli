# funky

Manage [Funky Cloud](https://platform.funky.dev) agents as code. Describe each agent in a YAML
file, then `funky apply` creates or updates it — like Terraform, for agent configs.

## Install

```bash
brew install funkyhq/tap/funky
```

Or download a binary for macOS, Linux or Windows from the
[latest release](https://github.com/funkyhq/cli/releases/latest), or, with Go installed:

```bash
go install github.com/funkyhq/cli/cmd/funky@latest
```

`funky --version` shows the installed version.

## Quick start

```bash
funky login      # opens your browser: sign in, pick a project, approve this machine
funky init       # creates agents/
$EDITOR agents/researcher.yaml
funky apply      # shows the plan, asks, then creates or updates
```

## Agent files

One file per agent config in `agents/`. The fields are the API's agent config fields:

```yaml
# agents/researcher.yaml
inference:
  provider: anthropic
  model: claude-sonnet-5
  maxTokens: 2048
systemPrompt: You are an autonomous agent in a fresh Linux sandbox.
network:
  type: allowlist
  domains: [github.com]
packages:
  pip: [pandas==2.2.0]
setupCommands: []
credentials: []
metadata: { team: research }
```

`inference`, `systemPrompt` and `network` are required; the rest default to empty. An agent is
named by its file name, or by a `name:` field. The name is stored in the config's metadata as
`funky.dev/name`, which is how the next `apply` finds the config to update.

`funky apply`:

- creates configs for new files, updates configs whose file changed (sending only the changed
  fields, so each apply adds at most one version), and leaves the rest alone;
- pins each update to the version it planned against, so a config edited elsewhere in the
  meantime fails with a conflict instead of being overwritten;
- never archives anything: removing a file leaves its config in place.

Flags: `--dry-run` shows the plan only; `--auto-approve` skips the confirmation.

## Authentication

`funky login` signs the CLI in as you, in one project — run it again to switch projects. The
login lasts 90 days, lives in your OS keychain, and can be revoked with `funky logout` or from
Settings in the web app. `funky whoami` shows who and where the CLI is acting.

On a machine without a browser, `funky login --no-browser` prints a link and asks for the code
the page shows.

In CI, set `FUNKY_API_KEY` to a key from https://platform.funky.dev/api-keys instead; it takes
precedence over a login.

## Development

```bash
go test ./...
go run ./cmd/funky --help
```

`FUNKY_API_URL`, `FUNKY_CONSOLE_URL` and `FUNKY_PLATFORM_URL` point the CLI at non-production
services; `FUNKY_CONFIG_DIR` moves where the login is saved.

## Releasing

Push a version tag; the release workflow builds every platform with
[GoReleaser](https://goreleaser.com), publishes a GitHub release and updates the Homebrew tap:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

A tag with a suffix (`v0.2.0-rc.1`) becomes a pre-release and skips the tap. To try the build
locally: `goreleaser release --snapshot --clean` (output in `dist/`).
