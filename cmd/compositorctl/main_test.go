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
