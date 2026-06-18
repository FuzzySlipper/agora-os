package main

import (
	"strings"
	"testing"
)

func TestBuildLaunchRequestDefaultsRole(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "toplevel" {
		t.Fatalf("got role %q, want toplevel", req.Role)
	}
}

func TestBuildLaunchRequestAcceptsRole(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--role", "panel"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "panel" {
		t.Fatalf("got role %q, want panel", req.Role)
	}
}

func TestBuildLaunchRequestAcceptsURL(t *testing.T) {
	t.Parallel()

	req, err := buildLaunchRequest([]string{"--url", "http://127.0.0.1:7780/shell/dist/desktop/", "--role", "panel"})
	if err != nil {
		t.Fatalf("buildLaunchRequest returned error: %v", err)
	}
	if req.Role != "panel" {
		t.Fatalf("got role %q, want panel", req.Role)
	}
	if req.Command != "webview-launcher --url 'http://127.0.0.1:7780/shell/dist/desktop/'" {
		t.Fatalf("command = %q", req.Command)
	}
}

func TestBuildLaunchRequestRejectsCmdAndURL(t *testing.T) {
	t.Parallel()

	_, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--url", "http://example.test"})
	if err == nil {
		t.Fatal("expected mutual exclusion error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildLaunchRequestRejectsInvalidRole(t *testing.T) {
	t.Parallel()

	_, err := buildLaunchRequest([]string{"--cmd", "webview-launcher --url http://example.test", "--role", "invalid"})
	if err == nil {
		t.Fatal("expected invalid role error")
	}
	if !strings.Contains(err.Error(), "valid values: toplevel, panel, dock, background, overlay") {
		t.Fatalf("error does not list valid values: %v", err)
	}
}

func TestBuildSetViewPropertyRequestParsesAlwaysOnTop(t *testing.T) {
	t.Parallel()

	req, err := buildSetViewPropertyRequest([]string{"--surface", "view-42", "--always-on-top", "true"})
	if err != nil {
		t.Fatalf("buildSetViewPropertyRequest returned error: %v", err)
	}
	if req.SurfaceID != "view-42" {
		t.Fatalf("surface = %q, want view-42", req.SurfaceID)
	}
	if req.Properties["always_on_top"] != true {
		t.Fatalf("always_on_top = %#v, want true", req.Properties["always_on_top"])
	}
}

func TestBuildSetViewPropertyRequestRequiresProperty(t *testing.T) {
	t.Parallel()

	_, err := buildSetViewPropertyRequest([]string{"--surface", "view-42"})
	if err == nil {
		t.Fatal("expected missing property error")
	}
	if !strings.Contains(err.Error(), "at least one property flag is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}
