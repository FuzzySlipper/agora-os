// Package peercred extracts kernel-verified identity from Unix socket
// connections via SO_PEERCRED. This is the enforcement mechanism behind the
// design invariant that agent identity comes from the kernel, not from
// self-reporting.
package peercred

import (
	"fmt"
	"net"
	"syscall"
)

// PeerUID returns the effective uid of the process on the other end of a
// Unix socket connection, as reported by the kernel via SO_PEERCRED.
// This cannot be spoofed by the peer.
func PeerUID(conn net.Conn) (uint32, error) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peercred: not a Unix connection")
	}

	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peercred: %w", err)
	}

	var cred *syscall.Ucred
	var credErr error
	controlErr := raw.Control(func(fd uintptr) {
		cred, credErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if controlErr != nil {
		return 0, fmt.Errorf("peercred: %w", controlErr)
	}
	if credErr != nil {
		return 0, fmt.Errorf("peercred: %w", credErr)
	}

	return uint32(cred.Uid), nil
}
