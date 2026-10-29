// Package apply reconciles local agent files with the agent configs in
// Funky: plan compares them, apply creates or updates what differs.
package apply

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/funkyhq/cli/internal/funky"
	"github.com/funkyhq/cli/internal/project"
)

// API is the slice of the Funky client apply needs.
type API interface {
	ListAgentConfigs(ctx context.Context) ([]funky.AgentConfig, error)
	CreateAgentConfig(ctx context.Context, body any) (*funky.AgentConfig, error)
	UpdateAgentConfig(ctx context.Context, id string, body any) (*funky.AgentConfig, error)
}

type Action string

const (
	Create    Action = "create"
	Update    Action = "update"
	Unchanged Action = "unchanged"
)

// Change is what apply will do for one agent file.
type Change struct {
	Agent  project.Agent
	Action Action
	// Remote is the config the file manages; nil for Create.
	Remote *funky.AgentConfig
	// Fields lists the spec fields that differ, for Update.
	Fields []string
	// Body is the request to send; nil for Unchanged.
	Body map[string]any
}

// Plan matches each agent to the live config carrying its name and works
// out what to send. Archived configs are read-only, so they never match:
// a file whose config was archived plans a fresh create.
func Plan(ctx context.Context, api API, agents []project.Agent) ([]Change, error) {
	remotes, err := api.ListAgentConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing agent configs: %w", err)
	}
	byName := map[string][]funky.AgentConfig{}
	for _, r := range remotes {
		if r.ArchivedAt != "" {
			continue
		}
		if name := remoteName(r); name != "" {
			byName[name] = append(byName[name], r)
		}
	}

	changes := make([]Change, 0, len(agents))
	for _, agent := range agents {
		spec := agent.Spec()
		matches := byName[agent.Name]
		switch len(matches) {
		case 0:
			changes = append(changes, Change{Agent: agent, Action: Create, Body: spec})
			continue
		case 1:
		default:
			ids := make([]string, len(matches))
			for i, m := range matches {
				ids[i] = m.ID
			}
			return nil, fmt.Errorf("%s: %d live agent configs are named %q (%s); archive all but one",
				agent.File, len(matches), agent.Name, strings.Join(ids, ", "))
		}

		remote := matches[0]
		live, err := remoteSpec(remote)
		if err != nil {
			return nil, err
		}
		body := map[string]any{}
		var fields []string
		for field, want := range spec {
			if !equalJSON(want, live[field]) {
				body[field] = want
				fields = append(fields, field)
			}
		}
		if len(fields) == 0 {
			changes = append(changes, Change{Agent: agent, Action: Unchanged, Remote: &remote})
			continue
		}
		sort.Strings(fields)
		// Every field sent costs a version, so only changed ones go, pinned
		// to the version they were compared against.
		body["version"] = remote.Version
		changes = append(changes, Change{Agent: agent, Action: Update, Remote: &remote, Fields: fields, Body: body})
	}
	return changes, nil
}

// Result is the config a change produced, or left alone.
type Result struct {
	Change Change
	Config *funky.AgentConfig
}

// Apply sends each change in order and stops at the first failure,
// returning the results that succeeded before it.
func Apply(ctx context.Context, api API, changes []Change) ([]Result, error) {
	results := make([]Result, 0, len(changes))
	for _, ch := range changes {
		var cfg *funky.AgentConfig
		var err error
		switch ch.Action {
		case Create:
			cfg, err = api.CreateAgentConfig(ctx, ch.Body)
		case Update:
			cfg, err = api.UpdateAgentConfig(ctx, ch.Remote.ID, ch.Body)
		case Unchanged:
			cfg = ch.Remote
		}
		if err != nil {
			return results, fmt.Errorf("%s %s (%s): %w", ch.Action, ch.Agent.Name, ch.Agent.File, err)
		}
		results = append(results, Result{Change: ch, Config: cfg})
	}
	return results, nil
}

func remoteName(r funky.AgentConfig) string {
	var meta map[string]any
	if json.Unmarshal(r.Metadata, &meta) != nil {
		return ""
	}
	name, _ := meta[project.NameKey].(string)
	return name
}

// remoteSpec is the live config's spec fields, keyed like project.Agent.Spec.
func remoteSpec(r funky.AgentConfig) (map[string]any, error) {
	raw := map[string]json.RawMessage{
		"inference":     r.Inference,
		"systemPrompt":  r.SystemPrompt,
		"network":       r.Network,
		"packages":      r.Packages,
		"credentials":   r.Credentials,
		"setupCommands": r.SetupCommands,
		"metadata":      r.Metadata,
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		if len(v) == 0 {
			continue
		}
		var decoded any
		if err := json.Unmarshal(v, &decoded); err != nil {
			return nil, fmt.Errorf("agent config %s: decoding %s: %w", r.ID, k, err)
		}
		out[k] = decoded
	}
	return out, nil
}

// equalJSON compares a local value with a decoded remote one by their JSON
// meaning, so YAML's ints and JSON's float64s compare equal.
func equalJSON(local, remote any) bool {
	buf, err := json.Marshal(local)
	if err != nil {
		return false
	}
	var decoded any
	if err := json.Unmarshal(buf, &decoded); err != nil {
		return false
	}
	return reflect.DeepEqual(decoded, remote)
}
