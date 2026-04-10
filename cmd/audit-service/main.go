// Audit service: monitors filesystem activity by agent uid using fanotify.
// Streams structured events to stdout and an append-only log file.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
	"unsafe"

	"github.com/patch/agora-os/internal/schema"
)

const (
	logPath          = "/var/log/agent-os/audit.log"
	subscriberSock   = "/run/agent-os/audit.sock"
	agentUIDBase     = 60000
	agentUIDMax      = 61000
	ringSize         = 1024
)

func main() {
	watchPaths := os.Args[1:]
	if len(watchPaths) == 0 {
		watchPaths = []string{"/var/lib/agents"}
	}

	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		log.Fatalf("open log: %v", err)
	}
	defer logFile.Close()

	// Initialize fanotify.
	// FAN_CLASS_CONTENT gives us file access events with the fd of the accessed file.
	// FAN_REPORT_FID would give us filesystem-level events without needing an fd,
	// but requires kernel 5.9+ and more complex event parsing.
	fan, err := fanotifyInit()
	if err != nil {
		log.Fatalf("fanotify_init: %v", err)
	}
	defer syscall.Close(fan)

	for _, path := range watchPaths {
		if err := fanotifyMark(fan, path); err != nil {
			log.Fatalf("fanotify_mark %s: %v", path, err)
		}
		log.Printf("watching: %s", path)
	}

	os.MkdirAll("/run/agent-os", 0755)

	broker := NewBroker(ringSize)
	go serveSubscribers(subscriberSock, broker)

	log.Println("audit service running")

	// Graceful shutdown
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		syscall.Close(fan)
		os.Exit(0)
	}()

	buf := make([]byte, 4096)
	for {
		n, err := syscall.Read(fan, buf)
		if err != nil {
			log.Fatalf("read: %v", err)
		}

		events := parseFanotifyEvents(buf[:n])
		for _, ev := range events {
			// Only log events from agent uids
			if ev.AgentUID < agentUIDBase || ev.AgentUID >= agentUIDMax {
				continue
			}

			b, _ := json.Marshal(ev)
			line := append(b, '\n')
			fmt.Print(string(line))  // stdout for real-time consumers
			logFile.Write(line)      // append-only log
			broker.Publish(line)     // fan out to socket subscribers
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

// fanotifyEventMetadata mirrors the kernel struct
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

		// Read the path from /proc/self/fd/<fd>
		path := readFdPath(int(meta.Fd))
		syscall.Close(int(meta.Fd))

		// Look up the uid of the pid that caused the event
		uid := uidForPid(meta.Pid)

		action := maskToAction(meta.Mask)

		events = append(events, schema.AuditEvent{
			Timestamp: time.Now(),
			AgentUID:  uid,
			Action:    action,
			Resource:  path,
			Outcome:   "allowed",
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
	// /proc/<pid>/status contains Uid line: real, effective, saved, filesystem
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return 0
	}
	// Quick parse — find "Uid:\t<real>\t..."
	for i := 0; i < len(data)-4; i++ {
		if string(data[i:i+4]) == "Uid:" {
			var uid uint32
			fmt.Sscanf(string(data[i+5:]), "%d", &uid)
			return uid
		}
	}
	return 0
}

func maskToAction(mask uint64) string {
	switch {
	case mask&0x02 != 0:
		return "file_modify"
	case mask&0x08 != 0:
		return "file_close_write"
	case mask&0x20 != 0:
		return "file_open"
	default:
		return fmt.Sprintf("unknown:0x%x", mask)
	}
}
