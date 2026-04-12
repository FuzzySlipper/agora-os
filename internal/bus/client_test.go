package bus

import (
	"encoding/json"
	"net"
	"sync"
	"testing"
)

func TestClientReceiveConcurrent(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	c := newClient(client)

	go func() {
		enc := json.NewEncoder(server)
		_ = enc.Encode(Event{Topic: "audit.file.modify", Body: body("one")})
		_ = enc.Encode(Event{Topic: "audit.file.open", Body: body("two")})
	}()

	var wg sync.WaitGroup
	wg.Add(2)

	results := make(chan Event, 2)
	errs := make(chan error, 2)

	for range 2 {
		go func() {
			defer wg.Done()
			ev, err := c.Receive()
			if err != nil {
				errs <- err
				return
			}
			results <- ev
		}()
	}

	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("Receive returned error: %v", err)
		}
	}

	got := map[string]bool{}
	for ev := range results {
		got[ev.Topic] = true
	}

	if !got["audit.file.modify"] || !got["audit.file.open"] || len(got) != 2 {
		t.Fatalf("got topics %v, want both audit.file.modify and audit.file.open", got)
	}
}
