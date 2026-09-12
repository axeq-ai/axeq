// Package extension is the browser backend that drives a normal, user-run
// Chrome through the bridge extension in test-chrome-extension/ instead of a
// Playwright-launched browser. It exposes the same operations as the
// playwright package (PerformActions, Snapshot, Focused, Tabs, ...), reusing
// that package's data types, so the cmd/* commands can swap it in via --driver.
//
// Transport: this package serves HTTP on 127.0.0.1; the extension long-polls
// GET /cmd for the next command and posts replies to POST /result. Commands
// are matched to results by id.
package extension

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Bridge state for the single extension client this connector drives. Prepare
// populates srv and Teardown closes it, mirroring the playwright package's
// module-level session handles.
var (
	srv *http.Server

	mu      sync.Mutex
	queue   []queuedCmd                        // commands waiting for the extension to poll
	results = map[int64]chan json.RawMessage{} // per-command reply channels
	nextId  int64

	// notify wakes a blocked /cmd long-poll when a command is queued.
	notify = make(chan struct{}, 1)

	// lastPollMs is the unix-milli time of the extension's most recent /cmd
	// request — the "is an extension connected" signal.
	lastPollMs atomic.Int64
)

type queuedCmd struct {
	id   int64
	body []byte
}

// Prepare starts the bridge server on 127.0.0.1:port. The extension side is
// started by the user (load test-chrome-extension/ in Chrome and enable it in
// the popup); WaitForExtension blocks until it shows up.
func Prepare(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /cmd", handleCmd)
	mux.HandleFunc("POST /result", handleResult)

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("extension bridge: listening on 127.0.0.1:%d: %w", port, err)
	}

	srv = &http.Server{Handler: mux}
	go srv.Serve(ln) //nolint:errcheck // ErrServerClosed on Teardown is expected
	return nil
}

// Teardown stops the bridge server. The extension keeps polling until the user
// disables it in the popup; its polls simply start failing, which it tolerates.
func Teardown() error {
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// WaitForExtension blocks until the extension has polled /cmd at least once,
// or the timeout elapses.
func WaitForExtension(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if lastPollMs.Load() != 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no extension connected within %s — load test-chrome-extension/ at chrome://extensions and enable it in its popup", timeout)
}

// send queues a command for the extension and blocks until its reply arrives
// or the timeout elapses. cmd must be a JSON-marshalable object; the id is
// injected here.
func send(cmd map[string]any, timeout time.Duration) (json.RawMessage, error) {
	id := atomic.AddInt64(&nextId, 1)
	cmd["id"] = id
	body, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("extension bridge: marshalling command: %w", err)
	}

	ch := make(chan json.RawMessage, 1)
	mu.Lock()
	results[id] = ch
	queue = append(queue, queuedCmd{id: id, body: body})
	mu.Unlock()

	select {
	case notify <- struct{}{}:
	default:
	}

	select {
	case res := <-ch:
		return res, nil
	case <-time.After(timeout):
		mu.Lock()
		delete(results, id)
		for i, q := range queue {
			if q.id == id {
				queue = append(queue[:i], queue[i+1:]...)
				break
			}
		}
		mu.Unlock()
		return nil, fmt.Errorf("extension did not reply within %s — is it still connected and enabled?", timeout)
	}
}

// handleCmd is the extension's long-poll: it returns the next queued command,
// or 204 after ?wait= seconds (clamped to 0..25) with nothing to do.
func handleCmd(w http.ResponseWriter, r *http.Request) {
	lastPollMs.Store(time.Now().UnixMilli())

	waitS := 15
	if v, err := strconv.Atoi(r.URL.Query().Get("wait")); err == nil {
		waitS = max(0, min(v, 25))
	}
	timer := time.NewTimer(time.Duration(waitS) * time.Second)
	defer timer.Stop()

	for {
		if body := dequeue(); body != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Write(body) //nolint:errcheck
			return
		}
		select {
		case <-notify:
			// Loop: another poller may have raced us to the command.
		case <-timer.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

func dequeue() []byte {
	mu.Lock()
	defer mu.Unlock()
	if len(queue) == 0 {
		return nil
	}
	q := queue[0]
	queue = queue[1:]
	return q.body
}

// handleResult receives the extension's reply and routes it to the waiting
// send by id. Replies for commands nobody waits on anymore (timed out) drop.
func handleResult(w http.ResponseWriter, r *http.Request) {
	// Stitched full-page screenshots come back as base64 PNGs, so the body can
	// be large; the limit is just a backstop against a runaway peer.
	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var head struct {
		Id int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &head); err != nil {
		http.Error(w, fmt.Sprintf("bad result payload: %v", err), http.StatusBadRequest)
		return
	}

	mu.Lock()
	ch := results[head.Id]
	delete(results, head.Id)
	mu.Unlock()

	if ch != nil {
		ch <- body
	}
	w.WriteHeader(http.StatusNoContent)
}
