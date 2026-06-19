package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
	"github.com/patch/agora-os/internal/shellui"
)

const defaultThemeCSSURL = "/api/shell/theme.css"

type shellOptions struct {
	busSocket string
	configDir string
}

type shellThemePayload struct {
	Properties   map[string]any `json:"properties,omitempty"`
	CSSURL       string         `json:"css_url,omitempty"`
	WallpaperURL string         `json:"wallpaper_url,omitempty"`
}

type shellWidgetPayload struct {
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

type shellState struct {
	ConfigDir    string            `json:"config_dir"`
	Theme        shellThemePayload `json:"theme"`
	ThemeCSS     string            `json:"theme_css,omitempty"`
	ThemeCSSURL  string            `json:"theme_css_url,omitempty"`
	WallpaperURL string            `json:"wallpaper_url,omitempty"`
	Widgets      []shellWidgetInfo `json:"widgets"`
}

type shellWidgetInfo struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	HasIndex    bool     `json:"has_index"`
	HasManifest bool     `json:"has_manifest"`
	BusTopics   []string `json:"bus_topics,omitempty"`
}

func cmdShell(args []string, pretty bool) error {
	if len(args) == 0 {
		return fmt.Errorf("shell subcommand is required: set-theme, set-wallpaper, reset-theme, add-widget, remove-widget, list-widgets, state")
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		shellUsage()
		return nil
	}
	options := shellOptions{busSocket: schema.BusSocket, configDir: defaultShellConfigDir()}
	switch args[0] {
	case "set-theme":
		return cmdShellSetTheme(args[1:], options, pretty)
	case "set-wallpaper":
		return cmdShellSetWallpaper(args[1:], options, pretty)
	case "reset-theme":
		return cmdShellResetTheme(args[1:], options, pretty)
	case "add-widget":
		return cmdShellAddWidget(args[1:], options, pretty)
	case "remove-widget":
		return cmdShellRemoveWidget(args[1:], options, pretty)
	case "list-widgets":
		return cmdShellListWidgets(args[1:], options, pretty)
	case "state":
		return cmdShellState(args[1:], options, pretty)
	default:
		return fmt.Errorf("unknown shell subcommand: %s", args[0])
	}
}

func shellUsage() {
	fmt.Fprintf(os.Stderr, `Usage: compositorctl [--pretty] shell <subcommand> [flags]

Subcommands:
  set-theme      Publish shell.apply_theme with --properties JSON and/or --css FILE
  set-wallpaper  Publish shell.apply_theme with --url
  reset-theme    Publish shell.reset_theme
  add-widget     Install widget from --url path/URL and publish shell.widget.inject
  remove-widget  Remove installed widget and publish shell.widget.remove
  list-widgets   List widgets under the shell config directory
  state          Print current shell config state

Common flags:
  --bus-socket PATH  event bus socket (default /run/agent-os/bus.sock)
  --config-dir PATH  shell config dir (default /etc/agora-shell when present, otherwise $XDG_CONFIG_HOME/agora-shell or ~/.config/agora-shell)
`)
}

func shellFlagSet(name string, options *shellOptions) *flag.FlagSet {
	fs := flag.NewFlagSet("shell "+name, flag.ExitOnError)
	fs.StringVar(&options.busSocket, "bus-socket", options.busSocket, "event bus socket path")
	fs.StringVar(&options.configDir, "config-dir", options.configDir, "shell config directory")
	return fs
}

func cmdShellSetTheme(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("set-theme", &options)
	propertiesRaw := fs.String("properties", "", "theme custom properties JSON object")
	cssPath := fs.String("css", "", "theme.css file to copy into shell config")
	fs.Parse(args)
	payload, err := buildShellSetThemePayload(*propertiesRaw, *cssPath, options.configDir)
	if err != nil {
		return err
	}
	if payload.Properties == nil && payload.CSSURL == "" {
		return fmt.Errorf("--properties or --css is required")
	}
	if err := publishShellEvent(options.busSocket, schema.TopicShellApplyTheme, payload); err != nil {
		return err
	}
	if err := mergeShellThemeState(options.configDir, payload); err != nil {
		return err
	}
	return printJSONObject(map[string]any{"published": schema.TopicShellApplyTheme, "body": payload}, pretty)
}

func buildShellSetThemePayload(propertiesRaw, cssPath, configDir string) (shellThemePayload, error) {
	payload := shellThemePayload{}
	propertiesRaw = strings.TrimSpace(propertiesRaw)
	if propertiesRaw != "" {
		var properties map[string]any
		if err := json.Unmarshal([]byte(propertiesRaw), &properties); err != nil {
			return payload, fmt.Errorf("--properties must be a JSON object: %w", err)
		}
		if properties == nil {
			return payload, fmt.Errorf("--properties must be a JSON object")
		}
		for key, value := range properties {
			if !validThemePropertyName(key) {
				return payload, fmt.Errorf("--properties contains invalid custom property name %q", key)
			}
			if !validThemePropertyValue(value) {
				return payload, fmt.Errorf("--properties value for %q must be a non-empty string or number", key)
			}
		}
		payload.Properties = properties
	}
	cssPath = strings.TrimSpace(cssPath)
	if cssPath != "" {
		if err := copyFile(cssPath, filepath.Join(configDir, "theme.css"), 0644); err != nil {
			return payload, fmt.Errorf("copy --css to theme.css: %w", err)
		}
		payload.CSSURL = defaultThemeCSSURL
	}
	return payload, nil
}

func cmdShellSetWallpaper(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("set-wallpaper", &options)
	wallpaperURL := fs.String("url", "", "wallpaper URL/path to publish")
	fs.Parse(args)
	payload, err := buildShellWallpaperPayload(*wallpaperURL)
	if err != nil {
		return err
	}
	if err := publishShellEvent(options.busSocket, schema.TopicShellApplyTheme, payload); err != nil {
		return err
	}
	if err := mergeShellThemeState(options.configDir, payload); err != nil {
		return err
	}
	return printJSONObject(map[string]any{"published": schema.TopicShellApplyTheme, "body": payload}, pretty)
}

func buildShellWallpaperPayload(wallpaperURL string) (shellThemePayload, error) {
	wallpaperURL = strings.TrimSpace(wallpaperURL)
	if wallpaperURL == "" {
		return shellThemePayload{}, fmt.Errorf("--url is required")
	}
	return shellThemePayload{WallpaperURL: wallpaperURL}, nil
}

func cmdShellResetTheme(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("reset-theme", &options)
	fs.Parse(args)
	if err := publishShellEvent(options.busSocket, schema.TopicShellResetTheme, map[string]any{"reset": true}); err != nil {
		return err
	}
	if err := clearShellThemeState(options.configDir); err != nil {
		return err
	}
	return printJSONObject(map[string]any{"published": schema.TopicShellResetTheme}, pretty)
}

func cmdShellAddWidget(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("add-widget", &options)
	name := fs.String("name", "", "widget name [a-zA-Z0-9_-]")
	widgetURL := fs.String("url", "", "widget source directory/file or http(s) URL")
	fs.Parse(args)
	payload, err := buildShellAddWidgetPayload(*name, *widgetURL, options.configDir)
	if err != nil {
		return err
	}
	if err := publishShellEvent(options.busSocket, schema.TopicShellWidgetInject, payload); err != nil {
		return err
	}
	return printJSONObject(map[string]any{"published": schema.TopicShellWidgetInject, "body": payload}, pretty)
}

func buildShellAddWidgetPayload(name, widgetURL, configDir string) (shellWidgetPayload, error) {
	name = strings.TrimSpace(name)
	widgetURL = strings.TrimSpace(widgetURL)
	if !validShellWidgetName(name) {
		return shellWidgetPayload{}, fmt.Errorf("--name must contain only letters, digits, underscore, or hyphen")
	}
	if widgetURL == "" {
		return shellWidgetPayload{}, fmt.Errorf("--url is required")
	}
	widgetParent := filepath.Join(configDir, "widgets")
	if err := os.MkdirAll(widgetParent, 0755); err != nil {
		return shellWidgetPayload{}, err
	}
	stagingDir, err := os.MkdirTemp(widgetParent, "."+name+"-staging-*")
	if err != nil {
		return shellWidgetPayload{}, err
	}
	stagingActive := true
	defer func() {
		if stagingActive {
			_ = os.RemoveAll(stagingDir)
		}
	}()
	if isHTTPURL(widgetURL) {
		if err := os.WriteFile(filepath.Join(stagingDir, "index.html"), []byte(remoteWidgetHTML(widgetURL, name)), 0644); err != nil {
			return shellWidgetPayload{}, err
		}
	} else {
		info, err := os.Stat(widgetURL)
		if err != nil {
			return shellWidgetPayload{}, fmt.Errorf("stat --url: %w", err)
		}
		if info.IsDir() {
			if err := copyDir(widgetURL, stagingDir); err != nil {
				return shellWidgetPayload{}, err
			}
		} else {
			if err := copyFile(widgetURL, filepath.Join(stagingDir, "index.html"), 0644); err != nil {
				return shellWidgetPayload{}, err
			}
		}
	}
	if err := normalizeWidgetManifest(stagingDir, name, widgetURL); err != nil {
		return shellWidgetPayload{}, err
	}
	if _, err := os.Stat(filepath.Join(stagingDir, "index.html")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return shellWidgetPayload{}, fmt.Errorf("widget source must provide index.html")
		}
		return shellWidgetPayload{}, err
	}
	widgetDir := filepath.Join(widgetParent, name)
	if err := replaceDir(stagingDir, widgetDir); err != nil {
		return shellWidgetPayload{}, err
	}
	stagingActive = false
	return shellWidgetPayload{Name: name, URL: widgetURL}, nil
}

func cmdShellRemoveWidget(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("remove-widget", &options)
	name := fs.String("name", "", "widget name [a-zA-Z0-9_-]")
	fs.Parse(args)
	payload, err := buildShellRemoveWidgetPayload(*name, options.configDir)
	if err != nil {
		return err
	}
	if err := publishShellEvent(options.busSocket, schema.TopicShellWidgetRemove, payload); err != nil {
		return err
	}
	return printJSONObject(map[string]any{"published": schema.TopicShellWidgetRemove, "body": payload}, pretty)
}

func buildShellRemoveWidgetPayload(name, configDir string) (shellWidgetPayload, error) {
	name = strings.TrimSpace(name)
	if !validShellWidgetName(name) {
		return shellWidgetPayload{}, fmt.Errorf("--name must contain only letters, digits, underscore, or hyphen")
	}
	widgetDir := filepath.Join(configDir, "widgets", name)
	if err := os.RemoveAll(widgetDir); err != nil {
		return shellWidgetPayload{}, err
	}
	return shellWidgetPayload{Name: name}, nil
}

func cmdShellListWidgets(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("list-widgets", &options)
	fs.Parse(args)
	widgets, err := listShellWidgets(options.configDir)
	if err != nil {
		return err
	}
	return printJSONObject(widgets, pretty)
}

func cmdShellState(args []string, defaults shellOptions, pretty bool) error {
	options := defaults
	fs := shellFlagSet("state", &options)
	fs.Parse(args)
	state, err := readShellState(options.configDir)
	if err != nil {
		return err
	}
	return printJSONObject(state, pretty)
}

func mergeShellThemeState(configDir string, update shellThemePayload) error {
	current, err := readShellThemeState(configDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if update.Properties != nil {
		if current.Properties == nil {
			current.Properties = map[string]any{}
		}
		for key, value := range update.Properties {
			current.Properties[key] = value
		}
	}
	if update.CSSURL != "" {
		current.CSSURL = update.CSSURL
	}
	if update.WallpaperURL != "" {
		current.WallpaperURL = update.WallpaperURL
	}
	return writeShellThemeState(configDir, current)
}

func clearShellThemeState(configDir string) error {
	if err := os.Remove(filepath.Join(configDir, "theme.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func readShellThemeState(configDir string) (shellThemePayload, error) {
	raw, err := os.ReadFile(filepath.Join(configDir, "theme.json"))
	if err != nil {
		return shellThemePayload{}, err
	}
	var theme shellThemePayload
	if err := json.Unmarshal(raw, &theme); err != nil {
		return shellThemePayload{}, err
	}
	return theme, nil
}

func writeShellThemeState(configDir string, theme shellThemePayload) error {
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(theme, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(filepath.Join(configDir, "theme.json"), raw, 0644)
}

func readShellState(configDir string) (shellState, error) {
	widgets, err := listShellWidgets(configDir)
	if err != nil {
		return shellState{}, err
	}
	state := shellState{ConfigDir: configDir, ThemeCSSURL: defaultThemeCSSURL, Widgets: widgets}
	if theme, err := readShellThemeState(configDir); err == nil {
		state.Theme = theme
		state.WallpaperURL = theme.WallpaperURL
	} else if !errors.Is(err, os.ErrNotExist) {
		return shellState{}, err
	}
	if raw, err := os.ReadFile(filepath.Join(configDir, "theme.css")); err == nil {
		state.ThemeCSS = string(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return shellState{}, err
	}
	return state, nil
}

func listShellWidgets(configDir string) ([]shellWidgetInfo, error) {
	widgetsDir := filepath.Join(configDir, "widgets")
	entries, err := os.ReadDir(widgetsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []shellWidgetInfo{}, nil
		}
		return nil, err
	}
	widgets := make([]shellWidgetInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validShellWidgetName(entry.Name()) {
			continue
		}
		widgetDir := filepath.Join(widgetsDir, entry.Name())
		info := shellWidgetInfo{Name: entry.Name(), Path: widgetDir}
		if _, err := os.Stat(filepath.Join(widgetDir, "index.html")); err == nil {
			info.HasIndex = true
		}
		manifestPath := filepath.Join(widgetDir, "manifest.json")
		if raw, err := os.ReadFile(manifestPath); err == nil {
			info.HasManifest = true
			info.BusTopics = manifestBusTopics(raw)
		}
		widgets = append(widgets, info)
	}
	sort.Slice(widgets, func(i, j int) bool { return widgets[i].Name < widgets[j].Name })
	return widgets, nil
}

func publishShellEvent(busSocket, topic string, body any) error {
	client, err := bus.Dial(busSocket)
	if err != nil {
		return fmt.Errorf("connect event bus %s: %w", busSocket, err)
	}
	defer client.Close()
	if err := client.Publish(topic, body); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	return nil
}

func defaultShellConfigDir() string {
	return defaultShellConfigDirWithShared(shellui.DefaultShellConfigDir)
}

func defaultShellConfigDirWithShared(sharedDir string) string {
	if info, err := os.Stat(sharedDir); err == nil && info.IsDir() {
		return sharedDir
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "agora-shell")
	}
	if home := strings.TrimSpace(os.Getenv("HOME")); home != "" {
		return filepath.Join(home, ".config", "agora-shell")
	}
	return filepath.Join(".", "agora-shell")
}

func validThemePropertyName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "--") {
		trimmed = strings.TrimPrefix(trimmed, "--")
	}
	if trimmed == "" {
		return false
	}
	for i, r := range trimmed {
		if i == 0 {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				continue
			}
			return false
		}
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func validThemePropertyValue(value any) bool {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v) != ""
	case float64:
		return true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return true
	default:
		return false
	}
}

func validShellWidgetName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return err
	}
	tmp := dst + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, dst)
}

func copyDir(src, dst string) error {
	rootInfo, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0755)
		}
		if strings.HasPrefix(entry.Name(), ".") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return copyFile(path, target, info.Mode().Perm())
	})
}

func replaceDir(stagingDir, widgetDir string) error {
	parent := filepath.Dir(widgetDir)
	backupDir, err := os.MkdirTemp(parent, "."+filepath.Base(widgetDir)+"-backup-*")
	if err != nil {
		return err
	}
	if err := os.Remove(backupDir); err != nil {
		return err
	}
	backupActive := false
	if _, err := os.Stat(widgetDir); err == nil {
		if err := os.Rename(widgetDir, backupDir); err != nil {
			return err
		}
		backupActive = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(stagingDir, widgetDir); err != nil {
		if backupActive {
			_ = os.Rename(backupDir, widgetDir)
		}
		return err
	}
	if backupActive {
		_ = os.RemoveAll(backupDir)
	}
	return nil
}

func normalizeWidgetManifest(widgetDir, name, source string) error {
	manifestPath := filepath.Join(widgetDir, "manifest.json")
	manifest := map[string]any{}
	if raw, err := os.ReadFile(manifestPath); err == nil {
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return fmt.Errorf("manifest.json is not valid JSON: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest["name"] = name
	if title, ok := manifest["title"].(string); !ok || strings.TrimSpace(title) == "" {
		manifest["title"] = name
	}
	manifest["source_url"] = source
	if _, ok := manifest["bus_topics"]; !ok {
		manifest["bus_topics"] = []string{"widget." + name + ".*"}
	}
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(manifestPath, raw, 0644)
}

func manifestBusTopics(raw []byte) []string {
	var manifest struct {
		BusTopics []string `json:"bus_topics"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return nil
	}
	return manifest.BusTopics
}

func isHTTPURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func remoteWidgetHTML(widgetURL, name string) string {
	return "<!doctype html>\n" +
		"<html><head><meta charset=\"utf-8\"><title>" + htmlEscape(name) + "</title></head>\n" +
		"<body style=\"margin:0\"><iframe src=\"" + htmlEscape(widgetURL) + "\" title=\"" + htmlEscape(name) + "\" style=\"border:0;width:100vw;height:100vh\"></iframe></body></html>\n"
}

func htmlEscape(value string) string {
	value = strings.ReplaceAll(value, "&", "&amp;")
	value = strings.ReplaceAll(value, "<", "&lt;")
	value = strings.ReplaceAll(value, ">", "&gt;")
	value = strings.ReplaceAll(value, "\"", "&quot;")
	return value
}

func printJSONObject(value any, pretty bool) error {
	enc := json.NewEncoder(os.Stdout)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(value)
}
