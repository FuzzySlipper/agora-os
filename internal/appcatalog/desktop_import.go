package appcatalog

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const DesktopImportTaskID = 3121

var candidateIDCleanPattern = regexp.MustCompile(`[^a-z0-9_-]+`)

type DesktopImportOptions struct {
	Dirs             []string
	IncludeHidden    bool
	IncludeNoDisplay bool
}

type DesktopCandidateArtifact struct {
	Version     int                `json:"version"`
	GeneratedBy string             `json:"generated_by"`
	Sources     []string           `json:"sources"`
	Candidates  []DesktopCandidate `json:"candidates"`
	Diagnostics []ImportDiagnostic `json:"diagnostics,omitempty"`
	Summary     ImportSummary      `json:"summary"`
}

type ImportSummary struct {
	FilesScanned      int `json:"files_scanned"`
	CandidatesEmitted int `json:"candidates_emitted"`
	Skipped           int `json:"skipped"`
	Diagnostics       int `json:"diagnostics"`
}

type ImportDiagnostic struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type DesktopCandidate struct {
	Entry   Entry           `json:"entry"`
	Desktop DesktopMetadata `json:"desktop"`
}

type DesktopMetadata struct {
	DesktopID       string          `json:"desktop_id"`
	Path            string          `json:"path"`
	Type            string          `json:"type,omitempty"`
	Exec            string          `json:"exec,omitempty"`
	ParsedArgv      []string        `json:"parsed_argv,omitempty"`
	FieldCodes      []string        `json:"field_codes,omitempty"`
	Terminal        bool            `json:"terminal"`
	NoDisplay       bool            `json:"no_display"`
	Hidden          bool            `json:"hidden"`
	DBusActivatable bool            `json:"dbus_activatable"`
	Actions         []DesktopAction `json:"actions,omitempty"`
	Categories      []string        `json:"categories,omitempty"`
	Icon            string          `json:"icon,omitempty"`
	Comment         string          `json:"comment,omitempty"`
	GenericName     string          `json:"generic_name,omitempty"`
	Keywords        []string        `json:"keywords,omitempty"`
	StartupWMClass  string          `json:"startup_wm_class,omitempty"`
	RiskFlags       []string        `json:"risk_flags,omitempty"`
	ExecParseError  string          `json:"exec_parse_error,omitempty"`
}

type DesktopAction struct {
	ID         string   `json:"id"`
	Name       string   `json:"name,omitempty"`
	Exec       string   `json:"exec,omitempty"`
	FieldCodes []string `json:"field_codes,omitempty"`
	RiskFlags  []string `json:"risk_flags,omitempty"`
}

type desktopFile struct {
	path   string
	groups map[string]map[string]string
}

func DefaultDesktopImportDirs() []string {
	seen := map[string]struct{}{}
	var dirs []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			return
		}
		if _, ok := seen[dir]; ok {
			return
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}
	if xdgHome := os.Getenv("XDG_DATA_HOME"); xdgHome != "" {
		add(filepath.Join(xdgHome, "applications"))
	} else if home := os.Getenv("HOME"); home != "" {
		add(filepath.Join(home, ".local/share/applications"))
	}
	dataDirs := os.Getenv("XDG_DATA_DIRS")
	if dataDirs == "" {
		dataDirs = "/usr/local/share:/usr/share"
	}
	for _, base := range filepath.SplitList(dataDirs) {
		add(filepath.Join(base, "applications"))
	}
	return dirs
}

func ImportDesktopCandidates(opts DesktopImportOptions) (DesktopCandidateArtifact, error) {
	dirs := append([]string(nil), opts.Dirs...)
	if len(dirs) == 0 {
		dirs = DefaultDesktopImportDirs()
	}
	artifact := DesktopCandidateArtifact{Version: 1, GeneratedBy: "compositorctl app import-desktop", Sources: append([]string(nil), dirs...)}
	seenPath := map[string]struct{}{}
	seenID := map[string]string{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			artifact.Diagnostics = append(artifact.Diagnostics, ImportDiagnostic{Path: dir, Reason: fmt.Sprintf("read dir: %v", err)})
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".desktop") {
				continue
			}
			path := filepath.Join(dir, entry.Name())
			cleanPath, err := filepath.Abs(path)
			if err == nil {
				path = cleanPath
			}
			if _, ok := seenPath[path]; ok {
				continue
			}
			seenPath[path] = struct{}{}
			artifact.Summary.FilesScanned++
			candidate, ok, diag := importDesktopFile(path, opts)
			if diag.Reason != "" {
				artifact.Diagnostics = append(artifact.Diagnostics, diag)
			}
			if !ok {
				artifact.Summary.Skipped++
				continue
			}
			if originalPath, duplicate := seenID[candidate.Entry.ID]; duplicate {
				artifact.Diagnostics = append(artifact.Diagnostics, ImportDiagnostic{Path: path, Reason: fmt.Sprintf("duplicate desktop candidate id %q; keeping %s", candidate.Entry.ID, originalPath)})
				artifact.Summary.Skipped++
				continue
			}
			seenID[candidate.Entry.ID] = path
			artifact.Candidates = append(artifact.Candidates, candidate)
		}
	}
	sort.SliceStable(artifact.Candidates, func(i, j int) bool { return artifact.Candidates[i].Entry.ID < artifact.Candidates[j].Entry.ID })
	artifact.Summary.CandidatesEmitted = len(artifact.Candidates)
	artifact.Summary.Diagnostics = len(artifact.Diagnostics)
	return artifact, nil
}

func (a DesktopCandidateArtifact) CatalogOverlay() (Catalog, error) {
	catalog := Catalog{Version: 1, Entries: make([]Entry, 0, len(a.Candidates))}
	for _, candidate := range a.Candidates {
		entry := candidate.Entry
		entry.Enabled = false
		entry.Command = ""
		if strings.TrimSpace(entry.Reason) == "" {
			entry.Reason = "imported from " + candidate.Desktop.DesktopID + "; review required before enabling (#3121)"
		}
		catalog.Entries = append(catalog.Entries, entry)
	}
	return Validate(catalog)
}

func MarshalDesktopImport(artifact DesktopCandidateArtifact, format string) ([]byte, error) {
	switch strings.TrimSpace(format) {
	case "", "candidates":
		return json.MarshalIndent(artifact, "", "  ")
	case "catalog-overlay":
		catalog, err := artifact.CatalogOverlay()
		if err != nil {
			return nil, err
		}
		return json.MarshalIndent(catalog, "", "  ")
	default:
		return nil, fmt.Errorf("unsupported import format %q", format)
	}
}

func importDesktopFile(path string, opts DesktopImportOptions) (DesktopCandidate, bool, ImportDiagnostic) {
	parsed, err := parseDesktopFile(path)
	if err != nil {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: err.Error()}
	}
	main := parsed.groups["Desktop Entry"]
	if main == nil {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "missing [Desktop Entry] group"}
	}
	if typ := main["Type"]; typ != "" && typ != "Application" {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "not an Application desktop entry"}
	}
	name := strings.TrimSpace(main["Name"])
	if name == "" {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "missing Name"}
	}
	hidden := parseDesktopBool(main["Hidden"])
	noDisplay := parseDesktopBool(main["NoDisplay"])
	if hidden && !opts.IncludeHidden {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "Hidden=true skipped"}
	}
	if noDisplay && !opts.IncludeNoDisplay {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "NoDisplay=true skipped"}
	}
	execLine := strings.TrimSpace(main["Exec"])
	dbus := parseDesktopBool(main["DBusActivatable"])
	if execLine == "" && !dbus {
		return DesktopCandidate{}, false, ImportDiagnostic{Path: path, Reason: "missing Exec"}
	}
	desktopID := filepath.Base(path)
	metadata := DesktopMetadata{
		DesktopID: desktopID, Path: path, Type: valueOrDefault(main["Type"], "Application"), Exec: execLine,
		Terminal: parseDesktopBool(main["Terminal"]), NoDisplay: noDisplay, Hidden: hidden, DBusActivatable: dbus,
		Categories: splitDesktopList(main["Categories"]), Icon: strings.TrimSpace(main["Icon"]), Comment: strings.TrimSpace(main["Comment"]),
		GenericName: strings.TrimSpace(main["GenericName"]), Keywords: splitDesktopList(main["Keywords"]), StartupWMClass: strings.TrimSpace(main["StartupWMClass"]),
	}
	metadata.ParsedArgv, metadata.ExecParseError = parseDesktopExec(execLine)
	metadata.FieldCodes = extractFieldCodes(execLine)
	metadata.Actions = parseDesktopActions(parsed.groups, splitDesktopList(main["Actions"]))
	metadata.RiskFlags = desktopRiskFlags(metadata)
	desc := strings.TrimSpace(metadata.Comment)
	if desc == "" {
		desc = strings.TrimSpace(metadata.GenericName)
	}
	entry := Entry{
		ID: candidateID(desktopID), Label: name, Description: desc, Icon: metadata.Icon,
		Tags: desktopTags(metadata.Categories, metadata.Keywords), Enabled: false,
		Reason: "imported from " + desktopID + "; review required before enabling (#3121)", DisabledTaskID: DesktopImportTaskID,
	}
	return DesktopCandidate{Entry: entry, Desktop: metadata}, true, ImportDiagnostic{}
}

func parseDesktopFile(path string) (desktopFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return desktopFile{}, err
	}
	defer f.Close()
	out := desktopFile{path: path, groups: map[string]map[string]string{}}
	group := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			group = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if group != "" && out.groups[group] == nil {
				out.groups[group] = map[string]string{}
			}
			continue
		}
		if group == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if bracket := strings.Index(key, "["); bracket >= 0 {
			key = key[:bracket]
		}
		out.groups[group][key] = strings.TrimSpace(value)
	}
	if err := scanner.Err(); err != nil {
		return desktopFile{}, err
	}
	return out, nil
}

func parseDesktopActions(groups map[string]map[string]string, actionIDs []string) []DesktopAction {
	var actions []DesktopAction
	for _, id := range actionIDs {
		group := groups["Desktop Action "+id]
		if group == nil {
			continue
		}
		execLine := strings.TrimSpace(group["Exec"])
		action := DesktopAction{ID: id, Name: strings.TrimSpace(group["Name"]), Exec: execLine, FieldCodes: extractFieldCodes(execLine)}
		action.RiskFlags = actionRiskFlags(action)
		actions = append(actions, action)
	}
	sort.SliceStable(actions, func(i, j int) bool { return actions[i].ID < actions[j].ID })
	return actions
}

func parseDesktopBool(raw string) bool {
	return strings.EqualFold(strings.TrimSpace(raw), "true")
}

func splitDesktopList(raw string) []string {
	parts := strings.Split(raw, ";")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func candidateID(desktopID string) string {
	base := strings.TrimSuffix(desktopID, ".desktop")
	base = strings.ToLower(base)
	base = strings.ReplaceAll(base, ".", "-")
	base = candidateIDCleanPattern.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-_")
	if base == "" {
		base = "unknown"
	}
	id := "desktop-" + base
	if len(id) > 64 {
		id = strings.TrimRight(id[:64], "-_")
	}
	return id
}

func desktopTags(categories, keywords []string) []string {
	seen := map[string]struct{}{"desktop-file": {}}
	out := []string{"desktop-file"}
	add := func(raw string) {
		tag := strings.ToLower(raw)
		tag = candidateIDCleanPattern.ReplaceAllString(tag, "-")
		tag = strings.Trim(tag, "-_")
		if tag == "" || len(tag) > 32 {
			return
		}
		if _, ok := seen[tag]; ok {
			return
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	for _, category := range categories {
		add(category)
	}
	for _, keyword := range keywords {
		add(keyword)
	}
	sort.Strings(out[1:])
	return out
}

func desktopRiskFlags(m DesktopMetadata) []string {
	flags := map[string]struct{}{}
	add := func(flag string) { flags[flag] = struct{}{} }
	if m.Terminal {
		add("terminal_required")
	}
	if m.Hidden {
		add("hidden")
	}
	if m.NoDisplay {
		add("no_display")
	}
	if m.DBusActivatable {
		add("dbus_activatable")
	}
	if len(m.Actions) > 0 {
		add("has_actions")
	}
	if m.ExecParseError != "" {
		add("exec_parse_error")
	}
	for _, code := range m.FieldCodes {
		classifyFieldCode(code, add)
	}
	return sortedFlags(flags)
}

func actionRiskFlags(action DesktopAction) []string {
	flags := map[string]struct{}{}
	add := func(flag string) { flags[flag] = struct{}{} }
	for _, code := range action.FieldCodes {
		classifyFieldCode(code, add)
	}
	return sortedFlags(flags)
}

func classifyFieldCode(code string, add func(string)) {
	switch code {
	case "%f", "%F", "%u", "%U":
		add("requires_argument_policy")
	case "%d", "%D", "%n", "%N", "%v", "%m":
		add("deprecated_field_code")
	case "%i", "%c", "%k":
		add("requires_review")
	case "%%":
		// Escaped percent is not a launch-time field expansion.
	default:
		if strings.HasPrefix(code, "%") {
			add("unknown_field_code")
		}
	}
}

func sortedFlags(flags map[string]struct{}) []string {
	out := make([]string, 0, len(flags))
	for flag := range flags {
		out = append(out, flag)
	}
	sort.Strings(out)
	return out
}

func extractFieldCodes(raw string) []string {
	seen := map[string]struct{}{}
	var out []string
	for i := 0; i < len(raw)-1; i++ {
		if raw[i] != '%' {
			continue
		}
		code := raw[i : i+2]
		if _, ok := seen[code]; ok {
			continue
		}
		seen[code] = struct{}{}
		out = append(out, code)
		i++
	}
	return out
}

func parseDesktopExec(raw string) ([]string, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ""
	}
	var argv []string
	var b strings.Builder
	var quote rune
	escaped := false
	flush := func() {
		if b.Len() > 0 {
			argv = append(argv, b.String())
			b.Reset()
		}
	}
	for _, r := range raw {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			continue
		}
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return argv, "unterminated quote in Exec"
	}
	flush()
	return argv, ""
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
