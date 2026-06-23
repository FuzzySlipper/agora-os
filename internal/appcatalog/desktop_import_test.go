package appcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeDesktopFixture(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestImportDesktopCandidatesCoversMetadataAndRisks(t *testing.T) {
	dir := t.TempDir()
	writeDesktopFixture(t, dir, "simple.desktop", `
[Desktop Entry]
Type=Application
Name=Simple App
GenericName=Simple Generic
Comment=Simple comment
Exec=/usr/bin/simple --flag
Icon=simple-icon
Categories=Utility;Development;
Keywords=alpha;beta;
StartupWMClass=simple-wm
`)
	writeDesktopFixture(t, dir, "url.desktop", `
[Desktop Entry]
Type=Application
Name=URL App
Exec=/usr/bin/url %u
Icon=url-icon
`)
	writeDesktopFixture(t, dir, "urls.desktop", `
[Desktop Entry]
Type=Application
Name=URLs App
Exec=/usr/bin/urls %U
`)
	writeDesktopFixture(t, dir, "file.desktop", `
[Desktop Entry]
Type=Application
Name=File App
Exec=/usr/bin/file %f
`)
	writeDesktopFixture(t, dir, "files.desktop", `
[Desktop Entry]
Type=Application
Name=Files App
Exec=/usr/bin/files %F
`)
	writeDesktopFixture(t, dir, "legacy.desktop", `
[Desktop Entry]
Type=Application
Name=Legacy App
Exec=/usr/bin/legacy %d %Z
`)
	writeDesktopFixture(t, dir, "terminal.desktop", `
[Desktop Entry]
Type=Application
Name=Terminal App
Exec=/usr/bin/terminal-app
Terminal=true
`)
	writeDesktopFixture(t, dir, "dbus.desktop", `
[Desktop Entry]
Type=Application
Name=DBus App
DBusActivatable=true
`)
	writeDesktopFixture(t, dir, "actions.desktop", `
[Desktop Entry]
Type=Application
Name=Actions App
Exec=/usr/bin/actions
Actions=new-window;open-url;

[Desktop Action new-window]
Name=New Window
Exec=/usr/bin/actions --new-window

[Desktop Action open-url]
Name=Open URL
Exec=/usr/bin/actions --open %U
`)
	writeDesktopFixture(t, dir, "hidden.desktop", `
[Desktop Entry]
Type=Application
Name=Hidden App
Exec=/usr/bin/hidden
Hidden=true
`)
	writeDesktopFixture(t, dir, "nodisplay.desktop", `
[Desktop Entry]
Type=Application
Name=NoDisplay App
Exec=/usr/bin/nodisplay
NoDisplay=true
`)

	artifact, err := ImportDesktopCandidates(DesktopImportOptions{Dirs: []string{dir}, IncludeHidden: true, IncludeNoDisplay: true})
	if err != nil {
		t.Fatalf("ImportDesktopCandidates: %v", err)
	}
	if artifact.Version != 1 || artifact.GeneratedBy != "compositorctl app import-desktop" || artifact.Summary.CandidatesEmitted != len(artifact.Candidates) {
		t.Fatalf("unexpected artifact header/summary: %+v", artifact)
	}
	byID := map[string]DesktopCandidate{}
	ids := make([]string, 0, len(artifact.Candidates))
	for _, candidate := range artifact.Candidates {
		if candidate.Entry.Enabled {
			t.Fatalf("candidate %s enabled; importer output must stay disabled", candidate.Entry.ID)
		}
		if candidate.Entry.Reason == "" || candidate.Entry.DisabledTaskID != 3121 || candidate.Desktop.Path == "" {
			t.Fatalf("candidate missing provenance/reason/task: %+v", candidate)
		}
		ids = append(ids, candidate.Entry.ID)
		byID[candidate.Entry.ID] = candidate
	}
	if !reflect.DeepEqual(ids, []string{"desktop-actions", "desktop-dbus", "desktop-file", "desktop-files", "desktop-hidden", "desktop-legacy", "desktop-nodisplay", "desktop-simple", "desktop-terminal", "desktop-url", "desktop-urls"}) {
		t.Fatalf("ids not deterministic: %v", ids)
	}
	for _, candidate := range artifact.Candidates {
		if candidate.Entry.ID != "desktop-dbus" && candidate.Desktop.Exec == "" {
			t.Fatalf("candidate %s missing raw Exec", candidate.Entry.ID)
		}
	}

	simple := byID["desktop-simple"]
	if simple.Entry.Label != "Simple App" || simple.Entry.Description != "Simple comment" || simple.Entry.Icon != "simple-icon" || simple.Desktop.StartupWMClass != "simple-wm" {
		t.Fatalf("simple metadata missing: %+v", simple)
	}
	if !reflect.DeepEqual(simple.Desktop.ParsedArgv, []string{"/usr/bin/simple", "--flag"}) {
		t.Fatalf("simple argv = %#v", simple.Desktop.ParsedArgv)
	}
	if !containsAll(simple.Entry.Tags, "desktop-file", "development", "utility", "alpha", "beta") {
		t.Fatalf("simple tags = %#v", simple.Entry.Tags)
	}
	for _, id := range []string{"desktop-url", "desktop-urls", "desktop-file", "desktop-files"} {
		if !contains(byID[id].Desktop.RiskFlags, "requires_argument_policy") {
			t.Fatalf("%s missing argument risk: %+v", id, byID[id].Desktop.RiskFlags)
		}
	}
	legacy := byID["desktop-legacy"]
	if !contains(legacy.Desktop.RiskFlags, "deprecated_field_code") || !contains(legacy.Desktop.RiskFlags, "unknown_field_code") {
		t.Fatalf("legacy risks = %#v fieldCodes=%#v", legacy.Desktop.RiskFlags, legacy.Desktop.FieldCodes)
	}
	if !contains(byID["desktop-terminal"].Desktop.RiskFlags, "terminal_required") {
		t.Fatalf("terminal risk missing: %+v", byID["desktop-terminal"].Desktop.RiskFlags)
	}
	if !contains(byID["desktop-dbus"].Desktop.RiskFlags, "dbus_activatable") {
		t.Fatalf("dbus risk missing: %+v", byID["desktop-dbus"].Desktop.RiskFlags)
	}
	if !contains(byID["desktop-hidden"].Desktop.RiskFlags, "hidden") || !contains(byID["desktop-nodisplay"].Desktop.RiskFlags, "no_display") {
		t.Fatalf("hidden/nodisplay risks missing")
	}
	actions := byID["desktop-actions"]
	if !contains(actions.Desktop.RiskFlags, "has_actions") || len(actions.Desktop.Actions) != 2 || actions.Desktop.Actions[1].ID != "open-url" || !contains(actions.Desktop.Actions[1].RiskFlags, "requires_argument_policy") {
		t.Fatalf("actions metadata missing: %+v", actions.Desktop.Actions)
	}
}

func TestImportDesktopCandidatesDefaultSkipsHiddenAndNoDisplay(t *testing.T) {
	dir := t.TempDir()
	writeDesktopFixture(t, dir, "visible.desktop", `[Desktop Entry]
Type=Application
Name=Visible
Exec=/usr/bin/visible`)
	writeDesktopFixture(t, dir, "hidden.desktop", `[Desktop Entry]
Type=Application
Name=Hidden
Exec=/usr/bin/hidden
Hidden=true`)
	writeDesktopFixture(t, dir, "nodisplay.desktop", `[Desktop Entry]
Type=Application
Name=NoDisplay
Exec=/usr/bin/nodisplay
NoDisplay=true`)
	artifact, err := ImportDesktopCandidates(DesktopImportOptions{Dirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Candidates) != 1 || artifact.Candidates[0].Entry.ID != "desktop-visible" {
		t.Fatalf("default candidates = %+v", artifact.Candidates)
	}
	if artifact.Summary.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", artifact.Summary.Skipped)
	}
}

func TestImportDesktopCandidatesRecordsParseErrorsAndCatalogOverlayValidates(t *testing.T) {
	dir := t.TempDir()
	writeDesktopFixture(t, dir, "badquote.desktop", `[Desktop Entry]
Type=Application
Name=Bad Quote
Exec=/usr/bin/bad "unterminated`)
	artifact, err := ImportDesktopCandidates(DesktopImportOptions{Dirs: []string{dir}})
	if err != nil {
		t.Fatal(err)
	}
	candidate := artifact.Candidates[0]
	if candidate.Desktop.ExecParseError == "" || !contains(candidate.Desktop.RiskFlags, "exec_parse_error") {
		t.Fatalf("parse error not recorded: %+v", candidate.Desktop)
	}
	payload, err := MarshalDesktopImport(artifact, "catalog-overlay")
	if err != nil {
		t.Fatalf("catalog overlay: %v", err)
	}
	var catalog Catalog
	if err := json.Unmarshal(payload, &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Entries) != 1 || catalog.Entries[0].Enabled || catalog.Entries[0].Command != "" {
		t.Fatalf("overlay entry not disabled: %+v", catalog.Entries)
	}
}

func TestImportDesktopCandidatesSkipsDuplicateGeneratedIDs(t *testing.T) {
	dir := t.TempDir()
	writeDesktopFixture(t, dir, "foo.bar.desktop", `[Desktop Entry]
Type=Application
Name=One
Exec=/usr/bin/one`)
	writeDesktopFixture(t, dir, "foo-bar.desktop", `[Desktop Entry]
Type=Application
Name=Two
Exec=/usr/bin/two`)
	artifact, err := ImportDesktopCandidates(DesktopImportOptions{Dirs: []string{dir}})
	if err != nil {
		t.Fatalf("ImportDesktopCandidates: %v", err)
	}
	if len(artifact.Candidates) != 1 || artifact.Candidates[0].Entry.Label != "Two" {
		t.Fatalf("duplicate handling should keep deterministic first sorted file: %+v", artifact.Candidates)
	}
	if artifact.Summary.Skipped != 1 || len(artifact.Diagnostics) != 1 || !strings.Contains(artifact.Diagnostics[0].Reason, "duplicate desktop candidate id") {
		t.Fatalf("duplicate diagnostic/summary missing: summary=%+v diagnostics=%+v", artifact.Summary, artifact.Diagnostics)
	}
}

func TestImportDesktopCandidatesUsesXDGPrecedenceForDuplicateIDs(t *testing.T) {
	userData := t.TempDir()
	systemData := t.TempDir()
	userApps := filepath.Join(userData, "applications")
	systemApps := filepath.Join(systemData, "applications")
	writeDesktopFixture(t, userApps, "demo.desktop", `[Desktop Entry]
Type=Application
Name=User Demo
Exec=/usr/bin/user-demo`)
	writeDesktopFixture(t, systemApps, "demo.desktop", `[Desktop Entry]
Type=Application
Name=System Demo
Exec=/usr/bin/system-demo`)
	t.Setenv("XDG_DATA_HOME", userData)
	t.Setenv("XDG_DATA_DIRS", systemData)

	artifact, err := ImportDesktopCandidates(DesktopImportOptions{})
	if err != nil {
		t.Fatalf("ImportDesktopCandidates: %v", err)
	}
	if len(artifact.Candidates) != 1 || artifact.Candidates[0].Entry.Label != "User Demo" {
		t.Fatalf("expected user desktop file to take precedence: %+v", artifact.Candidates)
	}
	if artifact.Summary.Skipped != 1 || len(artifact.Diagnostics) != 1 || !strings.Contains(artifact.Diagnostics[0].Reason, "duplicate desktop candidate id") {
		t.Fatalf("duplicate diagnostic/summary missing: summary=%+v diagnostics=%+v", artifact.Summary, artifact.Diagnostics)
	}
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func containsAll(list []string, values ...string) bool {
	for _, value := range values {
		if !contains(list, value) {
			return false
		}
	}
	return true
}
