package main

import (
	"log"
	"net"
	"os"
	"os/signal"
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

	bridge := compositor.New(busClient)

	os.MkdirAll(schema.SocketDir, 0755)
	os.Remove(schema.CompositorPluginSocket)
	os.Remove(schema.CompositorControlSocket)

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

	os.Chmod(schema.CompositorPluginSocket, 0666)
	os.Chmod(schema.CompositorControlSocket, 0666)

	log.Printf("compositor bridge plugin socket: %s", schema.CompositorPluginSocket)
	log.Printf("compositor bridge control socket: %s", schema.CompositorControlSocket)

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		log.Println("shutting down")
		pluginLn.Close()
		controlLn.Close()
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
