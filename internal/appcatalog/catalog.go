package appcatalog

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const DefaultCatalogFile = "/etc/agora-shell/app-catalog.json"

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var envKeyPattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

var deniedEnvKeys = map[string]struct{}{
	"LD_PRELOAD": {},
	"PATH":       {},
}

type Catalog struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

type Entry struct {
	ID             string            `json:"id"`
	Label          string            `json:"label"`
	Description    string            `json:"description,omitempty"`
	Icon           string            `json:"icon,omitempty"`
	Tags           []string          `json:"tags,omitempty"`
	Enabled        bool              `json:"enabled"`
	Reason         string            `json:"reason,omitempty"`
	Command        string            `json:"command,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	Role           string            `json:"role,omitempty"`
	ExpectedAppID  string            `json:"expected_app_id,omitempty"`
	ExpectedTitle  string            `json:"expected_title,omitempty"`
	Output         string            `json:"output,omitempty"`
	WaitSurface    *bool             `json:"wait_surface,omitempty"`
	WaitTimeoutMs  int               `json:"wait_timeout_ms,omitempty"`
	DisabledTaskID int               `json:"disabled_task_id,omitempty"`
}

type PublicEntry struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Description string   `json:"description,omitempty"`
	Icon        string   `json:"icon,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	State       string   `json:"state"`
	Reason      string   `json:"reason,omitempty"`
}

type ListResponse struct {
	Entries []PublicEntry `json:"entries"`
}

func Default() Catalog {
	wait := true
	return Catalog{Version: 1, Entries: []Entry{
		{
			ID:            "terminal",
			Label:         "Terminal",
			Description:   "Open a foot terminal",
			Icon:          "terminal",
			Tags:          []string{"system", "terminal"},
			Enabled:       true,
			Reason:        "default Agora shell tool",
			Command:       "foot",
			Role:          "toplevel",
			ExpectedAppID: "foot",
			WaitSurface:   &wait,
			WaitTimeoutMs: 10000,
		},
		{
			ID:             "browser",
			Label:          "Browser",
			Description:    "Open a graphical browser",
			Icon:           "browser",
			Tags:           []string{"network", "browser"},
			Enabled:        false,
			Reason:         "not installed/allowlisted on this host (#3024)",
			DisabledTaskID: 3024,
		},
	}}
}

func Load(path string) (Catalog, error) {
	base := Default()
	path = strings.TrimSpace(path)
	if path == "" {
		path = DefaultCatalogFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Validate(base)
		}
		return Catalog{}, fmt.Errorf("read app catalog %s: %w", path, err)
	}
	var host Catalog
	if err := json.Unmarshal(data, &host); err != nil {
		return Catalog{}, fmt.Errorf("decode app catalog %s: %w", path, err)
	}
	if err := validateUniqueIDs(host.Entries); err != nil {
		return Catalog{}, fmt.Errorf("validate app catalog %s: %w", path, err)
	}
	merged := Merge(base, host)
	return Validate(merged)
}

func validateUniqueIDs(entries []Entry) error {
	seen := map[string]struct{}{}
	for i, entry := range entries {
		id := strings.TrimSpace(entry.ID)
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate app catalog id %q", id)
		}
		seen[id] = struct{}{}
		if id == "" {
			return fmt.Errorf("entry %d id is required", i)
		}
	}
	return nil
}

func Merge(base, overlay Catalog) Catalog {
	if base.Version == 0 {
		base.Version = 1
	}
	out := Catalog{Version: base.Version, Entries: append([]Entry(nil), base.Entries...)}
	if overlay.Version != 0 {
		out.Version = overlay.Version
	}
	index := make(map[string]int, len(out.Entries))
	for i, entry := range out.Entries {
		index[entry.ID] = i
	}
	for _, entry := range overlay.Entries {
		if i, ok := index[entry.ID]; ok {
			out.Entries[i] = entry
		} else {
			index[entry.ID] = len(out.Entries)
			out.Entries = append(out.Entries, entry)
		}
	}
	return out
}

func Validate(c Catalog) (Catalog, error) {
	if c.Version == 0 {
		c.Version = 1
	}
	seen := map[string]struct{}{}
	for i := range c.Entries {
		entry := &c.Entries[i]
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Label = strings.TrimSpace(entry.Label)
		entry.Command = strings.TrimSpace(entry.Command)
		entry.Reason = strings.TrimSpace(entry.Reason)
		entry.Role = strings.TrimSpace(entry.Role)
		if entry.Role == "" {
			entry.Role = "toplevel"
		}
		if entry.WaitSurface == nil {
			v := entry.Enabled
			entry.WaitSurface = &v
		}
		if entry.Enabled && entry.WaitTimeoutMs <= 0 {
			entry.WaitTimeoutMs = 10000
		}
		if !idPattern.MatchString(entry.ID) {
			return Catalog{}, fmt.Errorf("entry %d has invalid id %q", i, entry.ID)
		}
		if _, ok := seen[entry.ID]; ok {
			return Catalog{}, fmt.Errorf("duplicate app catalog id %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		if entry.Label == "" {
			return Catalog{}, fmt.Errorf("entry %q label is required", entry.ID)
		}
		if !entry.Enabled {
			if entry.Reason == "" {
				return Catalog{}, fmt.Errorf("disabled entry %q reason is required", entry.ID)
			}
			continue
		}
		if entry.Command == "" {
			return Catalog{}, fmt.Errorf("enabled entry %q command is required", entry.ID)
		}
		if entry.Role != "toplevel" {
			return Catalog{}, fmt.Errorf("entry %q role %q unsupported in app catalog v1", entry.ID, entry.Role)
		}
		for key := range entry.Env {
			if !envKeyPattern.MatchString(key) {
				return Catalog{}, fmt.Errorf("entry %q env key %q is invalid", entry.ID, key)
			}
			if _, denied := deniedEnvKeys[key]; denied {
				return Catalog{}, fmt.Errorf("entry %q env key %q is not allowed", entry.ID, key)
			}
		}
	}
	sort.SliceStable(c.Entries, func(i, j int) bool { return c.Entries[i].ID < c.Entries[j].ID })
	return c, nil
}

func (c Catalog) PublicEntries() []PublicEntry {
	entries := make([]PublicEntry, 0, len(c.Entries))
	for _, entry := range c.Entries {
		state := "disabled"
		if entry.Enabled {
			state = "ready"
		}
		entries = append(entries, PublicEntry{ID: entry.ID, Label: entry.Label, Description: entry.Description, Icon: entry.Icon, Tags: append([]string(nil), entry.Tags...), State: state, Reason: entry.Reason})
	}
	return entries
}

func (c Catalog) Find(id string) (Entry, bool) {
	id = strings.TrimSpace(id)
	for _, entry := range c.Entries {
		if entry.ID == id {
			return entry, true
		}
	}
	return Entry{}, false
}
