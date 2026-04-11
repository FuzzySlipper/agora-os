// Package audit implements the Phase 1 audit service.
//
// It initializes fanotify, attributes events to agent uids, writes append-only
// log records, and fans serialized events out to local subscribers.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"github.com/patch/agora-os/internal/schema"
)

const (
	DefaultLogPath  = "/var/log/agent-os/audit.log"
	DefaultRingSize = 1024
)

// Config holds the composition-root configuration for the audit service.
type Config struct {
	WatchPaths     []string
	LogPath        string
	SubscriberSock string
	RingSize       int
	Stdout         io.Writer
}

// Service owns fanotify initialization, append-only logging, and subscriber
// fanout for audit events.
type Service struct {
	watchPaths     []string
	logPath        string
	subscriberSock string
	ringSize       int
	stdout         io.Writer
}

// New constructs a Service with defaults applied.
func New(cfg Config) *Service {
	watchPaths := cfg.WatchPaths
	if len(watchPaths) == 0 {
		watchPaths = []string{"/var/lib/agents"}
	}

	logPath := cfg.LogPath
	if logPath == "" {
		logPath = DefaultLogPath
	}

	subscriberSock := cfg.SubscriberSock
	if subscriberSock == "" {
		subscriberSock = schema.AuditSocket
	}

	ringSize := cfg.RingSize
	if ringSize == 0 {
		ringSize = DefaultRingSize
	}

	stdout := cfg.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}

	return &Service{
		watchPaths:     watchPaths,
		logPath:        logPath,
		subscriberSock: subscriberSock,
		ringSize:       ringSize,
		stdout:         stdout,
	}
}

// Run starts the audit service and blocks until shutdown or a fatal error.
func (s *Service) Run() error {
	logFile, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer logFile.Close()

	// Initialize fanotify.
	// FAN_CLASS_CONTENT gives us file access events with the fd of the accessed file.
	// FAN_REPORT_FID would give us filesystem-level events without needing an fd,
	// but requires kernel 5.9+ and more complex event parsing.
	fan, err := fanotifyInit()
	if err != nil {
		return fmt.Errorf("fanotify_init: %w", err)
	}
	defer syscall.Close(fan)

	for _, path := range s.watchPaths {
		if err := fanotifyMark(fan, path); err != nil {
			return fmt.Errorf("fanotify_mark %s: %w", path, err)
		}
		log.Printf("watching: %s", path)
	}

	if err := os.MkdirAll(schema.SocketDir, 0755); err != nil {
		return fmt.Errorf("mkdir %s: %w", schema.SocketDir, err)
	}

	broker := NewBroker(s.ringSize)
	go serveSubscribers(s.subscriberSock, broker)

	log.Println("audit service running")

	stopping := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		close(stopping)
		syscall.Close(fan)
	}()

	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(fan, buf)
		if err != nil {
			select {
			case <-stopping:
				return nil
			default:
				return fmt.Errorf("read: %w", err)
			}
		}

		events := parseFanotifyEvents(buf[:n])
		for _, ev := range events {
			// Only log events from agent uids.
			if ev.AgentUID < schema.AgentUIDBase || ev.AgentUID >= schema.AgentUIDMax {
				continue
			}

			b, _ := json.Marshal(ev)
			line := append(b, '\n')
			s.stdout.Write(line)
			logFile.Write(line)
			broker.Publish(line)
		}
	}
}

// --- fanotify syscall wrappers ---
// These wrap the raw syscalls since there's no standard Go fanotify library.
// The golang.org/x/sys/unix package provides the constants but not a high-level API.

func fanotifyInit() (int, error) {
	// FAN_CLASS_CONTENT = 0x04, O_RDONLY = 0x00, FAN_CLOEXEC = 0x01
	fd, _, errno := syscall.Syscall(
		syscall.SYS_FANOTIFY_INIT,
		0x04|0x01, // FAN_CLASS_CONTENT | FAN_CLOEXEC
		syscall.O_RDONLY|syscall.O_LARGEFILE,
		0,
	)
	if errno != 0 {
		return 0, errno
	}
	return int(fd), nil
}

func fanotifyMark(fan int, path string) error {
	// FAN_MARK_ADD = 0x01, FAN_MARK_FILESYSTEM = 0x100 (kernel 4.20+)
	// FAN_MODIFY = 0x02, FAN_CLOSE_WRITE = 0x08, FAN_OPEN = 0x20
	pathBytes, err := syscall.BytePtrFromString(path)
	if err != nil {
		return err
	}

	mask := uint64(0x02 | 0x08 | 0x20) // MODIFY | CLOSE_WRITE | OPEN
	_, _, errno := syscall.Syscall6(
		syscall.SYS_FANOTIFY_MARK,
		uintptr(fan),
		0x01|0x100, // FAN_MARK_ADD | FAN_MARK_FILESYSTEM
		uintptr(mask),
		uintptr(mask>>32),
		uintptr(unsafe.Pointer(pathBytes)),
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// fanotifyEventMetadata mirrors the kernel struct.
type fanotifyEventMetadata struct {
	EventLen    uint32
	Version     uint8
	Reserved    uint8
	MetadataLen uint16
	Mask        uint64
	Fd          int32
	Pid         int32
}

func parseFanotifyEvents(buf []byte) []schema.AuditEvent {
	var events []schema.AuditEvent
	offset := 0
	metaSize := int(unsafe.Sizeof(fanotifyEventMetadata{}))

	for offset+metaSize <= len(buf) {
		meta := (*fanotifyEventMetadata)(unsafe.Pointer(&buf[offset]))
		if meta.EventLen < uint32(metaSize) {
			break
		}

		// Read the path from /proc/self/fd/<fd>.
		path := readFdPath(int(meta.Fd))
		syscall.Close(int(meta.Fd))

		// Look up the uid of the pid that caused the event.
		uid := uidForPid(meta.Pid)

		events = append(events, schema.AuditEvent{
			Timestamp: time.Now(),
			AgentUID:  uid,
			Action:    maskToAction(meta.Mask),
			Resource:  path,
			Outcome:   schema.OutcomeAllowed,
		})

		offset += int(meta.EventLen)
	}
	return events
}

func readFdPath(fd int) string {
	link := fmt.Sprintf("/proc/self/fd/%d", fd)
	path, err := os.Readlink(link)
	if err != nil {
		return fmt.Sprintf("fd:%d", fd)
	}
	return path
}

func uidForPid(pid int32) uint32 {
	// /proc/<pid>/status contains Uid line: real, effective, saved, filesystem.
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	// Quick parse — find "Uid:\t<real>\t...".
	for i := 0; i < len(data)-4; i++ {
		if string(data[i:i+4]) == "Uid:" {
			var uid uint32
			fmt.Sscanf(string(data[i+5:]), "%d", &uid)
			return uid
		}
	}
	return 0
}

func maskToAction(mask uint64) schema.AuditAction {
	switch {
	case mask&0x02 != 0:
		return schema.ActionFileModify
	case mask&0x08 != 0:
		return schema.ActionFileCloseWrite
	case mask&0x20 != 0:
		return schema.ActionFileOpen
	default:
		return schema.AuditAction(fmt.Sprintf("unknown:0x%x", mask))
	}
}
