package appcatalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateDefaults(t *testing.T) {
	catalog, err := Validate(Default())
	if err != nil {
		t.Fatalf("Validate(Default): %v", err)
	}
	terminal, ok := catalog.Find("terminal")
	if !ok || !terminal.Enabled || terminal.Command == "" || terminal.WaitSurface == nil || !*terminal.WaitSurface {
		t.Fatalf("unexpected terminal entry: %+v ok=%v", terminal, ok)
	}
	entries := catalog.PublicEntries()
	if len(entries) == 0 || entries[0].ID == "" {
		t.Fatalf("public entries missing: %+v", entries)
	}
	for _, entry := range entries {
		if entry.ID == "terminal" && entry.State != "ready" {
			t.Fatalf("terminal state = %q, want ready", entry.State)
		}
		if entry.ID == "browser" && (entry.State != "disabled" || entry.Reason == "") {
			t.Fatalf("browser public entry = %+v", entry)
		}
	}
}

func TestValidateRejectsUnsafeCatalog(t *testing.T) {
	cases := []struct {
		name    string
		catalog Catalog
	}{
		{name: "bad id", catalog: Catalog{Entries: []Entry{{ID: "Bad ID", Label: "Bad", Enabled: false, Reason: "test"}}}},
		{name: "enabled missing command", catalog: Catalog{Entries: []Entry{{ID: "ok", Label: "OK", Enabled: true}}}},
		{name: "disabled missing reason", catalog: Catalog{Entries: []Entry{{ID: "ok", Label: "OK", Enabled: false}}}},
		{name: "env path denied", catalog: Catalog{Entries: []Entry{{ID: "ok", Label: "OK", Enabled: true, Command: "foot", Env: map[string]string{"PATH": "/tmp"}}}}},
		{name: "unsupported role", catalog: Catalog{Entries: []Entry{{ID: "ok", Label: "OK", Enabled: true, Command: "foot", Role: "panel"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Validate(tc.catalog); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestLoadMergesHostCatalog(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-catalog.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"entries":[{"id":"terminal","label":"Terminal Override","enabled":true,"command":"foot --title Test","role":"toplevel"},{"id":"editor","label":"Editor","enabled":false,"reason":"not reviewed"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	terminal, _ := catalog.Find("terminal")
	if terminal.Label != "Terminal Override" || terminal.Command != "foot --title Test" {
		t.Fatalf("terminal override not applied: %+v", terminal)
	}
	editor, ok := catalog.Find("editor")
	if !ok || editor.Enabled || editor.Reason == "" {
		t.Fatalf("editor extension missing: %+v ok=%v", editor, ok)
	}
}

func TestLoadRejectsDuplicateHostIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app-catalog.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"entries":[{"id":"terminal","label":"Terminal","enabled":true,"command":"foot"},{"id":"terminal","label":"Terminal 2","enabled":true,"command":"alacritty"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "duplicate app catalog id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

func TestLoadMissingUsesDefaults(t *testing.T) {
	catalog, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if _, ok := catalog.Find("terminal"); !ok {
		t.Fatal("default terminal missing")
	}
}
