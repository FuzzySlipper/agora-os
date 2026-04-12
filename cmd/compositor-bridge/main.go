package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/patch/agora-os/internal/bus"
	"github.com/patch/agora-os/internal/compositor"
	"github.com/patch/agora-os/internal/schema"
)

func main() {
	busClient, err := bus.Dial(schema.BusSocket)
	if err != nil {
		log.Fatalf("connect event bus: %v", err)
	}
	defer busClient.Close()

	compositorUID := envUint32("AGORA_COMPOSITOR_UID", 0)
	compositorGID := envUint32("AGORA_COMPOSITOR_GID", compositorUID)
	bridge := compositor.New(busClient, compositor.Config{AllowedPluginUID: compositorUID})

	if err := os.MkdirAll(schema.SocketDir, 0755); err != nil {
		log.Fatalf("mkdir %s: %v", schema.SocketDir, err)
	}
	_ = os.Remove(schema.CompositorPluginSocket)
	_ = os.Remove(schema.CompositorControlSocket)

	pluginLn, err := net.Listen("unix", schema.CompositorPluginSocket)
	if err != nil {
		log.Fatalf("listen plugin socket: %v", err)
	}
	defer pluginLn.Close()

	controlLn, err := net.Listen("unix", schema.CompositorControlSocket)
	if err != nil {
		log.Fatalf("listen control socket: %v", err)
	}
	defer controlLn.Close()

	mustConfigureSocket(schema.CompositorPluginSocket, 0660, 0, int(compositorGID))
	mustConfigureSocket(schema.CompositorControlSocket, 0660, 0, 0)

	log.Printf("compositor bridge plugin socket: %s (peer uid %d or root)", schema.CompositorPluginSocket, compositorUID)
	log.Printf("compositor bridge control socket: %s (root only)", schema.CompositorControlSocket)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		_ = pluginLn.Close()
		_ = controlLn.Close()
		os.Exit(0)
	}()

	go acceptLoop(pluginLn, func(conn net.Conn) {
		bridge.HandlePluginConn(conn)
	})
	acceptLoop(controlLn, func(conn net.Conn) {
		bridge.HandleControlConn(conn)
	})
}

func acceptLoop(ln net.Listener, handle func(net.Conn)) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			return
		}
		go handle(conn)
	}
}

func mustConfigureSocket(path string, mode os.FileMode, uid int, gid int) {
	if err := os.Chown(path, uid, gid); err != nil {
		log.Fatalf("chown %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		log.Fatalf("chmod %s: %v", path, err)
	}
}

func envUint32(name string, fallback uint32) uint32 {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		log.Fatalf("parse %s: %v", name, err)
	}
	return uint32(parsed)
}
