// Package project reads a funky project directory: the agents/ folder
// `funky init` creates, holding one agent config per YAML file.
package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// AgentsDir is the folder, relative to the project root, that holds agent files.
const AgentsDir = "agents"

// NameKey is the metadata key that ties a remote agent config to its local
// file. Agent configs have no name of their own, so apply finds the config
// a file manages by this key.
const NameKey = "funky.dev/name"

var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Agent is one agents/*.yaml file. Field names match the API's agent
// config wire shape, so the README's JSON examples translate one to one.
type Agent struct {
	// Name identifies the config across applies. Defaults to the file name
	// without its extension.
	Name          string              `yaml:"name"`
	Inference     *Inference          `yaml:"inference"`
	SystemPrompt  *string             `yaml:"systemPrompt"`
	Network       *Network            `yaml:"network"`
	Packages      map[string][]string `yaml:"packages"`
	Credentials   []Credential        `yaml:"credentials"`
	SetupCommands []string            `yaml:"setupCommands"`
	Metadata      map[string]any      `yaml:"metadata"`

	// File is the path the agent was read from.
	File string `yaml:"-"`
}

type Inference struct {
	Provider    string   `yaml:"provider" json:"provider"`
	Model       string   `yaml:"model" json:"model"`
	MaxTokens   *int     `yaml:"maxTokens" json:"maxTokens,omitempty"`
	Temperature *float64 `yaml:"temperature" json:"temperature,omitempty"`
}

type Network struct {
	Type    string    `yaml:"type" json:"type"`
	Domains *[]string `yaml:"domains" json:"domains,omitempty"`
}

type Credential struct {
	Type    string   `yaml:"type" json:"type"`
	Env     string   `yaml:"env" json:"env"`
	Secret  string   `yaml:"secret" json:"secret"`
	Domains []string `yaml:"domains" json:"domains"`
	Header  string   `yaml:"header" json:"header"`
	Value   string   `yaml:"value" json:"value"`
}

// Init creates the project structure under root. It reports whether
// anything was created; an existing agents/ folder is left untouched.
func Init(root string) (bool, error) {
	dir := filepath.Join(root, AgentsDir)
	info, err := os.Stat(dir)
	if err == nil {
		if !info.IsDir() {
			return false, fmt.Errorf("%s exists and is not a directory", dir)
		}
		return false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	return true, nil
}

// LoadAgents reads every *.yaml and *.yml file directly under
// root/agents, sorted by file name. Names must be unique.
func LoadAgents(root string) ([]Agent, error) {
	dir := filepath.Join(root, AgentsDir)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no %s directory in %s; run `funky init` first", AgentsDir, root)
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	var agents []Agent
	seen := map[string]string{}
	for _, e := range entries {
		ext := filepath.Ext(e.Name())
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		agent, err := loadAgent(path, strings.TrimSuffix(e.Name(), ext))
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[agent.Name]; dup {
			return nil, fmt.Errorf("%s: name %q is already used by %s", path, agent.Name, prev)
		}
		seen[agent.Name] = path
		agents = append(agents, agent)
	}
	return agents, nil
}

func loadAgent(path, defaultName string) (Agent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&a); err != nil {
		if errors.Is(err, io.EOF) {
			return Agent{}, fmt.Errorf("%s: file is empty", path)
		}
		return Agent{}, fmt.Errorf("%s: %w", path, err)
	}
	a.File = path
	if a.Name == "" {
		a.Name = defaultName
	}
	if err := a.validate(); err != nil {
		return Agent{}, fmt.Errorf("%s: %w", path, err)
	}
	return a, nil
}

// validate checks only what the CLI itself depends on; the API validates
// the rest and its messages are passed through.
func (a *Agent) validate() error {
	if !nameRE.MatchString(a.Name) {
		return fmt.Errorf("name %q must be 1-64 characters: letters, digits, _ and -", a.Name)
	}
	var missing []string
	if a.Inference == nil {
		missing = append(missing, "inference")
	}
	if a.SystemPrompt == nil {
		missing = append(missing, "systemPrompt")
	}
	if a.Network == nil {
		missing = append(missing, "network")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required field(s): %s", strings.Join(missing, ", "))
	}
	if _, ok := a.Metadata[NameKey]; ok {
		return fmt.Errorf("metadata key %q is set by funky from the agent's name; remove it", NameKey)
	}
	return nil
}

// Spec is the agent config body this file describes: every field the API
// accepts on create, with optional collections defaulted to empty and the
// name recorded in metadata.
func (a *Agent) Spec() map[string]any {
	metadata := map[string]any{NameKey: a.Name}
	for k, v := range a.Metadata {
		metadata[k] = v
	}
	packages := a.Packages
	if packages == nil {
		packages = map[string][]string{}
	}
	credentials := a.Credentials
	if credentials == nil {
		credentials = []Credential{}
	}
	setupCommands := a.SetupCommands
	if setupCommands == nil {
		setupCommands = []string{}
	}
	return map[string]any{
		"inference":     a.Inference,
		"systemPrompt":  *a.SystemPrompt,
		"network":       a.Network,
		"packages":      packages,
		"credentials":   credentials,
		"setupCommands": setupCommands,
		"metadata":      metadata,
	}
}
