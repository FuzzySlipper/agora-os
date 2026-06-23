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
		if entry.ID == "browser" {
			t.Fatalf("default catalog should not expose allowlist-only browser placeholder: %+v", entry)
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
	catalog, err := LoadWithOptions(path, LoadOptions{})
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
	if _, err := LoadWithOptions(path, LoadOptions{}); err == nil || !strings.Contains(err.Error(), "duplicate app catalog id") {
		t.Fatalf("expected duplicate id error, got %v", err)
	}
}

func TestLoadMissingUsesDefaults(t *testing.T) {
	catalog, err := LoadWithOptions(filepath.Join(t.TempDir(), "missing.json"), LoadOptions{})
	if err != nil {
		t.Fatalf("Load missing: %v", err)
	}
	if _, ok := catalog.Find("terminal"); !ok {
		t.Fatal("default terminal missing")
	}
}

func TestLoadIncludesInstalledDesktopAppsWhenEnabled(t *testing.T) {
	dir := t.TempDir()
	writeDesktopFixture(t, dir, "demo.desktop", `[Desktop Entry]
Type=Application
Name=Demo App
Comment=Human launchable app
Exec=/usr/bin/demo --flag %U
Icon=demo-icon
Categories=Utility;`)

	catalog, err := LoadWithOptions(filepath.Join(t.TempDir(), "missing.json"), LoadOptions{
		IncludeInstalledDesktopApps: true,
		DesktopImportOptions:        DesktopImportOptions{Dirs: []string{dir}},
	})
	if err != nil {
		t.Fatalf("LoadWithOptions desktop apps: %v", err)
	}
	entry, ok := catalog.Find("desktop-demo")
	if !ok {
		t.Fatalf("desktop app missing from catalog: %+v", catalog.PublicEntries())
	}
	if !entry.Enabled || entry.Command != "gtk-launch 'demo'" || entry.Reason != "installed desktop application" || entry.WaitSurface == nil || *entry.WaitSurface {
		t.Fatalf("desktop entry not launch-ready/sanitized: %+v", entry)
	}
	if !contains(entry.Tags, "installed") || !contains(entry.Tags, "desktop-file") {
		t.Fatalf("desktop tags missing provenance: %#v", entry.Tags)
	}
}
