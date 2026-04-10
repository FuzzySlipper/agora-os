package peercred

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUID(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "test.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Client connects in a goroutine; server accepts below.
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		conn, err := net.Dial("unix", sock)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		// Hold the connection open until the server is done.
		buf := make([]byte, 1)
		conn.Read(buf)
	}()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}

	uid, err := PeerUID(conn)
	if err != nil {
		t.Fatalf("PeerUID: %v", err)
	}

	want := uint32(os.Getuid())
	if uid != want {
		t.Errorf("PeerUID = %d, want %d", uid, want)
	}

	conn.Close()
	<-clientDone
}

func TestPeerUID_NonUnix(t *testing.T) {
	// A TCP connection should fail gracefully.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := net.Dial("tcp", ln.Addr().String())
		if conn != nil {
			buf := make([]byte, 1)
			conn.Read(buf)
			conn.Close()
		}
	}()

	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	_, err = PeerUID(conn)
	if err == nil {
		t.Fatal("expected error for TCP connection")
	}
}
