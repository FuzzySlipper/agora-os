package webview

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/schema"
)

//go:embed helper.py
var helperScript string

const (
	defaultAppID  = "io.agoraos.WebviewLauncher"
	defaultWidth  = 1280
	defaultHeight = 800
	defaultRole   = "toplevel"
	defaultPython = "/usr/bin/python3"

	helperEventCreated = "created"
	helperEventFocused = "focused"
	helperEventClosed  = "closed"
)

type Config struct {
	URL            string `json:"url,omitempty"`
	Path           string `json:"path,omitempty"`
	Title          string `json:"title,omitempty"`
	AppID          string `json:"app_id,omitempty"`
	Width          int    `json:"width,omitempty"`
	Height         int    `json:"height,omitempty"`
	Role           string `json:"role,omitempty"`
	Fullscreen     bool   `json:"fullscreen,omitempty"`
	AppCommandPort int    `json:"app_command_port,omitempty"`

	BusSocket string `json:"bus_socket,omitempty"`
}

type resolvedConfig struct {
	TargetURI      string
	Title          string
	AppID          string
	Width          int
	Height         int
	Role           string
	Fullscreen     bool
	AppCommandPort int
	BusSocket      string
}

type helperEvent struct {
	Event         string   `json:"event"`
	Title         string   `json:"title,omitempty"`
	PID           int      `json:"pid,omitempty"`
	Role          string   `json:"role,omitempty"`
	SurfaceKind   string   `json:"surface_kind,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Layer         string   `json:"layer,omitempty"`
	Anchors       []string `json:"anchors,omitempty"`
	ExclusiveZone *bool    `json:"exclusive_zone,omitempty"`
}

type launcherLifecycleEvent struct {
	Event         string   `json:"event"`
	SurfaceID     string   `json:"surface_id"`
	SurfaceKind   string   `json:"surface_kind"`
	AppID         string   `json:"app_id"`
	Title         string   `json:"title,omitempty"`
	PID           int      `json:"pid"`
	UID           uint32   `json:"uid"`
	GID           uint32   `json:"gid"`
	Role          string   `json:"role"`
	Width         int      `json:"width,omitempty"`
	Height        int      `json:"height,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Layer         string   `json:"layer,omitempty"`
	Anchors       []string `json:"anchors,omitempty"`
	ExclusiveZone *bool    `json:"exclusive_zone,omitempty"`
}

func Launch(ctx context.Context, cfg Config) error {
	resolved, err := normalizeConfig(cfg)
	if err != nil {
		return err
	}

	client, err := bus.Dial(resolved.BusSocket)
	if err != nil {
		return fmt.Errorf("connect event bus: %w", err)
	}
	defer client.Close()

	scriptPath, err := writeHelperScript()
	if err != nil {
		return err
	}
	defer os.Remove(scriptPath)

	cmd, stdout, stderr, err := startHelper(ctx, scriptPath, resolved)
	if err != nil {
		return err
	}
	defer terminateHelper(cmd)

	scanner := bufio.NewScanner(stdout)
	uid := uint32(os.Getuid())
	gid := uint32(os.Getgid())
	lastTitle := resolved.Title
	lastRole := resolved.Role
	createdSeen := false
	closedSeen := false
	helperPID := 0

	for scanner.Scan() {
		var ev helperEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			return fmt.Errorf("decode helper event: %w", err)
		}
		if ev.PID == 0 && cmd.Process != nil {
			ev.PID = cmd.Process.Pid
		}
		if ev.PID != 0 {
			helperPID = ev.PID
		}
		if ev.Title != "" {
			lastTitle = ev.Title
		}
		if ev.Role != "" {
			lastRole = ev.Role
		}
		if err := publishLifecycle(client, resolved, ev, uid, gid); err != nil {
			return err
		}
		switch ev.Event {
		case helperEventCreated:
			createdSeen = true
		case helperEventClosed:
			closedSeen = true
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read helper events: %w", err)
	}

	waitErr := cmd.Wait()
	if createdSeen && !closedSeen {
		fallbackPID := helperPID
		if fallbackPID == 0 && cmd.Process != nil {
			fallbackPID = cmd.Process.Pid
		}
		_ = publishLifecycle(client, resolved, helperEvent{Event: helperEventClosed, PID: fallbackPID, Title: lastTitle, Role: lastRole}, uid, gid)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("webview helper role %q: %w: %s", resolved.Role, waitErr, msg)
		}
		return fmt.Errorf("webview helper role %q: %w", resolved.Role, waitErr)
	}
	return nil
}

func normalizeConfig(cfg Config) (resolvedConfig, error) {
	targetURI, err := resolveTarget(cfg.URL, cfg.Path)
	if err != nil {
		return resolvedConfig{}, err
	}

	appID := strings.TrimSpace(cfg.AppID)
	if appID == "" {
		appID = defaultAppID
	}

	width := cfg.Width
	if width <= 0 {
		width = defaultWidth
	}

	height := cfg.Height
	if height <= 0 {
		height = defaultHeight
	}

	busSocket := strings.TrimSpace(cfg.BusSocket)
	if busSocket == "" {
		busSocket = schema.BusSocket
	}

	role, err := normalizeRole(cfg.Role)
	if err != nil {
		return resolvedConfig{}, err
	}
	if cfg.AppCommandPort < 0 || cfg.AppCommandPort > 65535 {
		return resolvedConfig{}, fmt.Errorf("app command port must be 0-65535, got %d", cfg.AppCommandPort)
	}

	title := strings.TrimSpace(cfg.Title)
	if title == "" {
		title = defaultTitle(targetURI)
	}

	return resolvedConfig{
		TargetURI:      targetURI,
		Title:          title,
		AppID:          appID,
		Width:          width,
		Height:         height,
		Role:           role,
		Fullscreen:     cfg.Fullscreen,
		AppCommandPort: cfg.AppCommandPort,
		BusSocket:      busSocket,
	}, nil
}

func normalizeRole(role string) (string, error) {
	role = strings.TrimSpace(role)
	if role == "" {
		return defaultRole, nil
	}
	switch role {
	case "toplevel", "panel", "dock", "background", "overlay":
		return role, nil
	default:
		return "", fmt.Errorf("unsupported webview role %q", role)
	}
}

func resolveTarget(rawURL, rawPath string) (string, error) {
	hasURL := strings.TrimSpace(rawURL) != ""
	hasPath := strings.TrimSpace(rawPath) != ""

	switch {
	case hasURL && hasPath:
		return "", errors.New("provide exactly one of --url or --path")
	case !hasURL && !hasPath:
		return "", errors.New("provide exactly one of --url or --path")
	case hasURL:
		parsed, err := url.Parse(strings.TrimSpace(rawURL))
		if err != nil {
			return "", fmt.Errorf("parse url: %w", err)
		}
		if parsed.Scheme == "" || parsed.Host == "" {
			return "", fmt.Errorf("url must be absolute: %q", rawURL)
		}
		return parsed.String(), nil
	default:
		abs, err := filepath.Abs(strings.TrimSpace(rawPath))
		if err != nil {
			return "", fmt.Errorf("resolve path: %w", err)
		}
		if _, err := os.Stat(abs); err != nil {
			return "", fmt.Errorf("stat path: %w", err)
		}
		return (&url.URL{Scheme: "file", Path: abs}).String(), nil
	}
}

func defaultTitle(targetURI string) string {
	parsed, err := url.Parse(targetURI)
	if err != nil {
		return "Agora Webview"
	}
	if parsed.Scheme == "file" {
		base := filepath.Base(parsed.Path)
		if base != "" && base != "." && base != string(filepath.Separator) {
			return base
		}
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return "Agora Webview"
}

func writeHelperScript() (string, error) {
	file, err := os.CreateTemp("", "agora-webview-helper-*.py")
	if err != nil {
		return "", fmt.Errorf("create helper script: %w", err)
	}
	if _, err := file.WriteString(helperScript); err != nil {
		file.Close()
		return "", fmt.Errorf("write helper script: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close helper script: %w", err)
	}
	if err := os.Chmod(file.Name(), 0700); err != nil {
		return "", fmt.Errorf("chmod helper script: %w", err)
	}
	return file.Name(), nil
}

func helperArgs(scriptPath string, cfg resolvedConfig) []string {
	args := []string{
		scriptPath,
		"--uri", cfg.TargetURI,
		"--app-id", cfg.AppID,
		"--width", strconv.Itoa(cfg.Width),
		"--height", strconv.Itoa(cfg.Height),
		"--title", cfg.Title,
		"--role", cfg.Role,
	}
	if cfg.Fullscreen {
		args = append(args, "--fullscreen")
	}
	if cfg.AppCommandPort > 0 {
		args = append(args, "--app-command-port", strconv.Itoa(cfg.AppCommandPort))
	}
	return args
}

func helperPython() string {
	if override := strings.TrimSpace(os.Getenv("AGORA_WEBVIEW_PYTHON")); override != "" {
		return override
	}
	return defaultPython
}

func startHelper(ctx context.Context, scriptPath string, cfg resolvedConfig) (*exec.Cmd, *bufio.Reader, *bytes.Buffer, error) {
	cmd := exec.CommandContext(ctx, helperPython(), helperArgs(scriptPath, cfg)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("helper stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Env = helperEnv(os.Environ(), cfg)
	if err := cmd.Start(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, nil, nil, fmt.Errorf("start helper role %q: %w: %s", cfg.Role, err, msg)
		}
		return nil, nil, nil, fmt.Errorf("start helper role %q: %w", cfg.Role, err)
	}
	return cmd, bufio.NewReader(stdout), stderr, nil
}

func helperEnv(base []string, cfg resolvedConfig) []string {
	env := append([]string(nil), base...)
	if cfg.AppCommandPort > 0 {
		env = append(env, "AGORA_APP_COMMAND_PORT="+strconv.Itoa(cfg.AppCommandPort))
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "GDK_BACKEND=") {
			return append(env, "PYTHONUNBUFFERED=1")
		}
	}
	return append(env, "GDK_BACKEND=wayland", "PYTHONUNBUFFERED=1")
}

func terminateHelper(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		return
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
}

func publishLifecycle(client *bus.Client, cfg resolvedConfig, ev helperEvent, uid, gid uint32) error {
	topic, eventName, ok := lifecycleMapping(ev.Event)
	if !ok {
		return fmt.Errorf("unknown helper event: %s", ev.Event)
	}
	pid := ev.PID
	if pid <= 0 {
		pid = os.Getpid()
	}
	title := ev.Title
	if title == "" {
		title = cfg.Title
	}
	body := schema.CompositorBusEvent{
		Surface: schema.CompositorSurface{
			ID:          surfaceIDForEvent(pid, lifecycleRole(cfg, ev)),
			SurfaceKind: lifecycleSurfaceKind(cfg, ev),
			AppID:       cfg.AppID,
			Title:       title,
			Role:        lifecycleRole(cfg, ev),
			Geometry:    &schema.SurfaceGeometry{Width: cfg.Width, Height: cfg.Height},
			PixelSize:   &schema.SurfaceGeometry{Width: cfg.Width, Height: cfg.Height},
			LayerShell:  layerShellMetadata(cfg, ev),
		},
		Client: schema.CompositorClientIdentity{
			PID: int32(pid),
			UID: uid,
			GID: gid,
		},
		Event: eventName,
	}
	if err := client.Publish(topic, body); err != nil {
		return fmt.Errorf("publish %s: %w", topic, err)
	}
	if err := emitLauncherLifecycle(body, cfg.Width, cfg.Height); err != nil {
		return err
	}
	return nil
}

func emitLauncherLifecycle(body schema.CompositorBusEvent, width, height int) error {
	event := launcherLifecycleEvent{
		Event:       string(body.Event),
		SurfaceID:   body.Surface.ID,
		SurfaceKind: body.Surface.SurfaceKind,
		AppID:       body.Surface.AppID,
		Title:       body.Surface.Title,
		PID:         int(body.Client.PID),
		UID:         body.Client.UID,
		GID:         body.Client.GID,
		Role:        body.Surface.Role,
		Width:       width,
		Height:      height,
	}
	if body.Surface.LayerShell != nil {
		event.Namespace = body.Surface.LayerShell.Namespace
		event.Layer = body.Surface.LayerShell.Layer
		event.Anchors = append([]string(nil), body.Surface.LayerShell.Anchors...)
		event.ExclusiveZone = body.Surface.LayerShell.ExclusiveZone
	}
	if err := json.NewEncoder(os.Stdout).Encode(event); err != nil {
		return fmt.Errorf("emit lifecycle stdout: %w", err)
	}
	return nil
}

func layerShellMetadata(cfg resolvedConfig, ev helperEvent) *schema.LayerShellSurfaceMetadata {
	if lifecycleSurfaceKind(cfg, ev) != schema.SurfaceKindLayerShell {
		return nil
	}
	return &schema.LayerShellSurfaceMetadata{
		Namespace:     ev.Namespace,
		Layer:         ev.Layer,
		Anchors:       append([]string(nil), ev.Anchors...),
		ExclusiveZone: ev.ExclusiveZone,
	}
}

func lifecycleRole(cfg resolvedConfig, ev helperEvent) string {
	if ev.Role != "" {
		return ev.Role
	}
	if cfg.Role != "" {
		return cfg.Role
	}
	return defaultRole
}

func lifecycleSurfaceKind(cfg resolvedConfig, ev helperEvent) string {
	if ev.SurfaceKind != "" {
		return ev.SurfaceKind
	}
	if lifecycleRole(cfg, ev) == "toplevel" {
		return schema.SurfaceKindXDGView
	}
	return schema.SurfaceKindLayerShell
}

func surfaceIDForEvent(pid int, role string) string {
	if role != "" && role != "toplevel" {
		return fmt.Sprintf("layer-shell-%d", pid)
	}
	return surfaceIDForPID(pid)
}

func lifecycleMapping(event string) (topic string, eventName schema.CompositorSurfaceEventName, ok bool) {
	switch event {
	case helperEventCreated:
		return schema.TopicCompositorAdvisorySurfaceCreated, schema.SurfaceEventMapped, true
	case helperEventFocused:
		return schema.TopicCompositorAdvisorySurfaceFocused, schema.SurfaceEventFocused, true
	case helperEventClosed:
		return schema.TopicCompositorAdvisorySurfaceDestroyed, schema.SurfaceEventUnmapped, true
	default:
		return "", "", false
	}
}

func surfaceIDForPID(pid int) string {
	return fmt.Sprintf("webview-%d", pid)
}
