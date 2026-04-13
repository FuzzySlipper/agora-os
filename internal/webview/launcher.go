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

	helperEventCreated = "created"
	helperEventFocused = "focused"
	helperEventClosed  = "closed"
)

type Config struct {
	URL    string
	Path   string
	Title  string
	AppID  string
	Width  int
	Height int

	BusSocket string
}

type resolvedConfig struct {
	TargetURI string
	Title     string
	AppID     string
	Width     int
	Height    int
	BusSocket string
}

type helperEvent struct {
	Event string `json:"event"`
	Title string `json:"title,omitempty"`
	PID   int    `json:"pid,omitempty"`
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
		_ = publishLifecycle(client, resolved, helperEvent{Event: helperEventClosed, PID: fallbackPID, Title: lastTitle}, uid, gid)
	}
	if waitErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return fmt.Errorf("webview helper: %w: %s", waitErr, msg)
		}
		return fmt.Errorf("webview helper: %w", waitErr)
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

	title := strings.TrimSpace(cfg.Title)
	if title == "" {
		title = defaultTitle(targetURI)
	}

	return resolvedConfig{
		TargetURI: targetURI,
		Title:     title,
		AppID:     appID,
		Width:     width,
		Height:    height,
		BusSocket: busSocket,
	}, nil
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

func startHelper(ctx context.Context, scriptPath string, cfg resolvedConfig) (*exec.Cmd, *bufio.Reader, *bytes.Buffer, error) {
	args := []string{
		scriptPath,
		"--uri", cfg.TargetURI,
		"--app-id", cfg.AppID,
		"--width", strconv.Itoa(cfg.Width),
		"--height", strconv.Itoa(cfg.Height),
		"--title", cfg.Title,
	}
	cmd := exec.CommandContext(ctx, "python3", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("helper stdout: %w", err)
	}
	stderr := &bytes.Buffer{}
	cmd.Stderr = stderr
	cmd.Env = helperEnv(os.Environ())
	if err := cmd.Start(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, nil, nil, fmt.Errorf("start helper: %w: %s", err, msg)
		}
		return nil, nil, nil, fmt.Errorf("start helper: %w", err)
	}
	return cmd, bufio.NewReader(stdout), stderr, nil
}

func helperEnv(base []string) []string {
	for _, entry := range base {
		if strings.HasPrefix(entry, "GDK_BACKEND=") {
			return append(append([]string(nil), base...), "PYTHONUNBUFFERED=1")
		}
	}
	return append(append([]string(nil), base...), "GDK_BACKEND=wayland", "PYTHONUNBUFFERED=1")
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
			ID:    surfaceIDForPID(pid),
			AppID: cfg.AppID,
			Title: title,
			Role:  "toplevel",
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
	return nil
}

func lifecycleMapping(event string) (topic string, eventName schema.CompositorSurfaceEventName, ok bool) {
	switch event {
	case helperEventCreated:
		return schema.TopicCompositorSurfaceCreated, schema.SurfaceEventMapped, true
	case helperEventFocused:
		return schema.TopicCompositorSurfaceFocused, schema.SurfaceEventFocused, true
	case helperEventClosed:
		return schema.TopicCompositorSurfaceDestroyed, schema.SurfaceEventUnmapped, true
	default:
		return "", "", false
	}
}

func surfaceIDForPID(pid int) string {
	return fmt.Sprintf("webview-%d", pid)
}
