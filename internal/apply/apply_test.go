package apply

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/funkyhq/cli/internal/funky"
	"github.com/funkyhq/cli/internal/project"
)

// fakeAPI is an in-memory /v1/agent-configs with the real API's update
// semantics: partial replacement, a version bump per update, and 409 on a
// stale version.
type fakeAPI struct {
	mu      sync.Mutex
	configs []map[string]any
	updates []map[string]any
	pageLen int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer test-key" {
		w.WriteHeader(401)
		fmt.Fprint(w, `{"type":"error","error":{"type":"authentication_error","message":"invalid or missing credential"}}`)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/agent-configs":
		start := 0
		if after := r.URL.Query().Get("after"); after != "" {
			for i, c := range f.configs {
				if c["id"] == after {
					start = i + 1
				}
			}
		}
		end := min(start+f.pageLen, len(f.configs))
		page := map[string]any{"data": f.configs[start:end], "hasMore": end < len(f.configs)}
		if end > start {
			page["lastId"] = f.configs[end-1]["id"]
		}
		json.NewEncoder(w).Encode(page)
	case r.Method == "POST" && r.URL.Path == "/v1/agent-configs":
		body["id"] = fmt.Sprintf("ac_%d", len(f.configs)+1)
		body["version"] = 1
		f.configs = append(f.configs, body)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(body)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/v1/agent-configs/"):
		id := strings.TrimPrefix(r.URL.Path, "/v1/agent-configs/")
		for _, c := range f.configs {
			if c["id"] != id {
				continue
			}
			if v, ok := body["version"]; ok && v != float64(c["version"].(int)) {
				w.WriteHeader(409)
				fmt.Fprint(w, `{"type":"error","error":{"type":"conflict_error","message":"stale version"}}`)
				return
			}
			f.updates = append(f.updates, body)
			for k, v := range body {
				if k != "version" {
					c[k] = v
				}
			}
			c["version"] = c["version"].(int) + 1
			json.NewEncoder(w).Encode(c)
			return
		}
		w.WriteHeader(404)
	default:
		w.WriteHeader(404)
	}
}

func setup(t *testing.T, files map[string]string) (*fakeAPI, *funky.Client, string) {
	t.Helper()
	root := t.TempDir()
	project.Init(root)
	for name, body := range files {
		os.WriteFile(filepath.Join(root, project.AgentsDir, name), []byte(body), 0o644)
	}
	api := &fakeAPI{pageLen: 1} // one per page exercises pagination
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return api, funky.NewClient(srv.URL+"/", funky.StaticToken("test-key")), root
}

func run(t *testing.T, client *funky.Client, root string) []Change {
	t.Helper()
	agents, err := project.LoadAgents(root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := Plan(context.Background(), client, agents)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), client, changes); err != nil {
		t.Fatal(err)
	}
	return changes
}

func actions(changes []Change) string {
	var s []string
	for _, c := range changes {
		s = append(s, c.Agent.Name+":"+string(c.Action))
	}
	return strings.Join(s, " ")
}

const researcher = `
inference: {provider: anthropic, model: claude-sonnet-5, maxTokens: 2048}
systemPrompt: research
network: {type: allowlist, domains: []}
metadata: {team: core}
`

func TestApplyIsIdempotentAndUpdatesOnlyChangedFields(t *testing.T) {
	api, client, root := setup(t, map[string]string{
		"researcher.yaml": researcher,
		"reviewer.yaml":   strings.Replace(researcher, "research", "review", 1),
	})

	if got := actions(run(t, client, root)); got != "researcher:create reviewer:create" {
		t.Fatalf("first apply = %s", got)
	}
	// The allowlist's empty domains must survive omitempty.
	if d, ok := api.configs[0]["network"].(map[string]any)["domains"]; !ok || len(d.([]any)) != 0 {
		t.Fatalf("network = %v; want domains: []", api.configs[0]["network"])
	}

	if got := actions(run(t, client, root)); got != "researcher:unchanged reviewer:unchanged" {
		t.Fatalf("second apply = %s; want no changes", got)
	}

	os.WriteFile(filepath.Join(root, "agents", "reviewer.yaml"),
		[]byte(strings.Replace(researcher, "research", "review harder", 1)), 0o644)
	changes := run(t, client, root)
	if got := actions(changes); got != "researcher:unchanged reviewer:update" {
		t.Fatalf("third apply = %s", got)
	}
	if strings.Join(changes[1].Fields, ",") != "systemPrompt" {
		t.Errorf("fields = %v; want only systemPrompt", changes[1].Fields)
	}
	want := map[string]any{"systemPrompt": "review harder", "version": float64(1)}
	if fmt.Sprint(api.updates) != fmt.Sprint([]map[string]any{want}) {
		t.Errorf("update bodies = %v; want [%v]", api.updates, want)
	}
	if len(api.configs) != 2 || api.configs[1]["version"] != 2 {
		t.Errorf("configs = %v; want 2 configs, reviewer at v2", api.configs)
	}
}

func TestPlanSkipsArchivedAndForeignConfigs(t *testing.T) {
	api, client, root := setup(t, map[string]string{"researcher.yaml": researcher})
	api.configs = []map[string]any{
		{"id": "old", "version": 3, "archivedAt": "2026-01-01T00:00:00Z", "metadata": map[string]any{project.NameKey: "researcher"}},
		{"id": "console-made", "version": 1, "metadata": map[string]any{"name": "researcher"}},
		{"id": "no-meta", "version": 1},
	}
	if got := actions(run(t, client, root)); got != "researcher:create" {
		t.Fatalf("apply = %s; want a fresh create", got)
	}
}

func TestPlanRejectsAmbiguousName(t *testing.T) {
	api, client, root := setup(t, map[string]string{"researcher.yaml": researcher})
	meta := map[string]any{project.NameKey: "researcher"}
	api.configs = []map[string]any{{"id": "a", "version": 1, "metadata": meta}, {"id": "b", "version": 1, "metadata": meta}}
	agents, _ := project.LoadAgents(root)
	_, err := Plan(context.Background(), client, agents)
	if err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Fatalf("err = %v; want both ids named", err)
	}
}

func TestApplySurfacesConflict(t *testing.T) {
	api, client, root := setup(t, map[string]string{"researcher.yaml": researcher})
	api.configs = []map[string]any{{"id": "ac_1", "version": 1, "metadata": map[string]any{project.NameKey: "researcher"}}}
	agents, _ := project.LoadAgents(root)
	changes, err := Plan(context.Background(), client, agents)
	if err != nil {
		t.Fatal(err)
	}
	api.configs[0]["version"] = 2 // someone edited it after the plan
	_, err = Apply(context.Background(), client, changes)
	var apiErr *funky.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Type != "conflict_error" {
		t.Fatalf("err = %v; want a 409 conflict_error", err)
	}
}

func TestAuthErrorIsReadable(t *testing.T) {
	_, client, root := setup(t, map[string]string{"researcher.yaml": researcher})
	client.Auth = funky.StaticToken("wrong")
	agents, _ := project.LoadAgents(root)
	_, err := Plan(context.Background(), client, agents)
	if err == nil || !strings.Contains(err.Error(), "invalid or missing credential") {
		t.Fatalf("err = %v", err)
	}
}
