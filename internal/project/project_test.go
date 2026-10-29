package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAgent(t *testing.T, root, file, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, AgentsDir, file), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

const minimal = `
inference: {provider: anthropic, model: claude-sonnet-5}
systemPrompt: hi
network: {type: none}
`

func TestInitCreatesAgentsDirOnce(t *testing.T) {
	root := t.TempDir()
	created, err := Init(root)
	if err != nil || !created {
		t.Fatalf("first Init = %v, %v; want true, nil", created, err)
	}
	if info, err := os.Stat(filepath.Join(root, AgentsDir)); err != nil || !info.IsDir() {
		t.Fatalf("agents dir missing: %v", err)
	}
	created, err = Init(root)
	if err != nil || created {
		t.Fatalf("second Init = %v, %v; want false, nil", created, err)
	}
}

func TestInitRefusesFileNamedAgents(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, AgentsDir), nil, 0o644)
	if _, err := Init(root); err == nil {
		t.Fatal("Init over a file: want error")
	}
}

func TestLoadAgentsWithoutInit(t *testing.T) {
	_, err := LoadAgents(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "funky init") {
		t.Fatalf("err = %v; want a hint to run funky init", err)
	}
}

func TestLoadAgents(t *testing.T) {
	root := t.TempDir()
	Init(root)
	writeAgent(t, root, "b.yml", minimal)
	writeAgent(t, root, "a.yaml", "name: alpha\n"+minimal)
	writeAgent(t, root, "notes.md", "ignored")

	agents, err := LoadAgents(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 2 || agents[0].Name != "alpha" || agents[1].Name != "b" {
		t.Fatalf("agents = %+v; want alpha (a.yaml) then b", agents)
	}

	spec := agents[1].Spec()
	if got := spec["metadata"].(map[string]any)[NameKey]; got != "b" {
		t.Errorf("metadata name = %v; want b", got)
	}
	if spec["packages"] == nil || spec["credentials"] == nil || spec["setupCommands"] == nil {
		t.Errorf("optional collections should default to empty, got %+v", spec)
	}
}

func TestLoadAgentsErrors(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"missing fields": {map[string]string{"a.yaml": "systemPrompt: hi\n"}, "inference, network"},
		"unknown field":  {map[string]string{"a.yaml": minimal + "sytemPrompt: typo\n"}, "sytemPrompt"},
		"bad name":       {map[string]string{"a.yaml": "name: has space\n" + minimal}, "has space"},
		"empty file":     {map[string]string{"a.yaml": ""}, "empty"},
		"duplicate name": {map[string]string{"a.yaml": "name: x\n" + minimal, "b.yaml": "name: x\n" + minimal}, "already used"},
		"reserved key":   {map[string]string{"a.yaml": minimal + "metadata: {funky.dev/name: y}\n"}, NameKey},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			Init(root)
			for f, body := range tc.files {
				writeAgent(t, root, f, body)
			}
			_, err := LoadAgents(root)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v; want it to mention %q", err, tc.want)
			}
		})
	}
}
