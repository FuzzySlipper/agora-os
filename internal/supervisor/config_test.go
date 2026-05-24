package supervisor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/patch/agora-os/internal/schema"
)

func TestDefaultConfigContainsProfilesAndGrants(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Profiles) == 0 {
		t.Fatal("expected built-in profiles")
	}
	if len(cfg.Grants) == 0 {
		t.Fatal("expected built-in grants")
	}
	profiles, err := NewProfileRegistry(cfg.Profiles)
	if err != nil {
		t.Fatalf("default profiles should build registry: %v", err)
	}
	if _, ok := profiles.Get("ui-observer"); !ok {
		t.Fatal("default config should include ui-observer profile")
	}
	repoSearch, ok := profiles.Get("repo-search")
	if !ok {
		t.Fatal("default config should include repo-search profile")
	}
	if repoSearch.Runtime != schema.RuntimeDeterministic || repoSearch.CPUQuota != "50%" || repoSearch.MemoryMax != "1G" || repoSearch.NetAccess != schema.NetLocalOnly {
		t.Fatalf("repo-search defaults do not match task spec: %+v", repoSearch)
	}
	repoInspector, ok := profiles.Get("repo-inspector")
	if !ok {
		t.Fatal("default config should include repo-inspector profile")
	}
	if repoInspector.Runtime != schema.RuntimeLocalLLM || repoInspector.CPUQuota != "50%" || repoInspector.MemoryMax != "1G" || repoInspector.NetAccess != schema.NetLocalOnly {
		t.Fatalf("repo-inspector defaults do not match task spec: %+v", repoInspector)
	}
	grants := NewGrantRegistry(cfg.Grants)
	if !grants.AllowedToRequest(60010, "repo-search") || !grants.AllowedToRequest(60010, "repo-inspector") {
		t.Fatalf("3PO uid 60010 should be allowed to request repo-search and repo-inspector")
	}
}

func TestLoadConfigReadsProfilesAndGrants(t *testing.T) {
	cfg := Config{
		Profiles: []schema.WorkerProfile{{
			Profile:         "repo-search",
			Runtime:         schema.RuntimeDeterministic,
			Tools:           []string{"find", "grep"},
			NetAccess:       schema.NetLocalOnly,
			MaxLeaseSeconds: 120,
			ReusePolicy:     schema.ReuseSession,
		}},
		Grants: []schema.ProfileGrant{{
			RequesterUID:         60010,
			AllowedProfiles:      []string{"repo-search"},
			MaxConcurrentWorkers: 1,
			MaxLeaseSeconds:      120,
		}},
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agent-supervisor.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got := loaded.Profiles[0].Profile; got != "repo-search" {
		t.Fatalf("profile = %q, want repo-search", got)
	}
	if got := loaded.Grants[0].RequesterUID; got != 60010 {
		t.Fatalf("requester uid = %d, want 60010", got)
	}
}

func TestLoadConfigRejectsEmptyConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected empty config to be rejected")
	}
}
