package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/patch/agora-os/internal/webview"
)

func main() {
	var cfg webview.Config

	flag.StringVar(&cfg.URL, "url", "", "remote URL to open in the webview")
	flag.StringVar(&cfg.Path, "path", "", "local HTML file to open in the webview")
	flag.StringVar(&cfg.Title, "title", "", "initial window title")
	flag.StringVar(&cfg.AppID, "app-id", "", "Wayland/GTK application id")
	flag.IntVar(&cfg.Width, "width", 1280, "initial window width in pixels")
	flag.IntVar(&cfg.Height, "height", 800, "initial window height in pixels")
	flag.StringVar(&cfg.Role, "role", "toplevel", "webview shell role: toplevel, panel, dock, background, overlay")
	flag.BoolVar(&cfg.Fullscreen, "fullscreen", false, "fullscreen the toplevel webview window")
	flag.BoolVar(&cfg.Undecorated, "undecorated", false, "disable toplevel window decorations")
	flag.IntVar(&cfg.AppCommandPort, "app-command-port", 0, "loopback port for typed app-command readiness endpoint (0 disables)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := webview.Launch(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}
