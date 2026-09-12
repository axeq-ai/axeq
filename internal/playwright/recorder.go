package playwright

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mxschmitt/playwright-go"
)

// Settling window: after a Pause request, keep capturing until the page has been
// visually unchanged this long, or until the hard cap elapses — whichever first.
const (
	settleQuiet = 1 * time.Second
	settleMax   = 5 * time.Second
)

type recState int

const (
	stateRecording recState = iota // capturing normally
	stateSettling                  // pause requested; still capturing until settled
	statePaused                    // not capturing
)

// Recorder captures the page as a stream of constant-rate PNG frames, which can
// later be assembled into a video. Frames are numbered contiguously and only
// captured while not paused, so paused stretches (e.g. while waiting on the
// agent) don't appear in the output — the result plays as continuous activity.
//
// It is stateful (a background ticker goroutine + state), so unlike most of this
// package it is used through methods on a handle.
type Recorder struct {
	framesDir string
	interval  time.Duration

	mu          sync.Mutex
	state       recState
	settleStart time.Time // when the current pause was requested
	lastChange  time.Time // when the frame last changed during settling
	lastFrame   []byte    // previous frame bytes, for change detection

	stop chan struct{}
	done chan struct{}
}

// StartRecording begins capturing the active tab into framesDir at the given
// fps. It follows the active tab, so frames track tab switches.
func StartRecording(framesDir string, fps int) (*Recorder, error) {
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating frames dir: %w", err)
	}

	r := &Recorder{
		framesDir: framesDir,
		interval:  time.Second / time.Duration(fps),
		state:     stateRecording,
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
	}
	go r.loop()
	return r, nil
}

func (r *Recorder) loop() {
	defer close(r.done)

	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	frame := 0
	for {
		select {
		case <-r.stop:
			return
		case <-ticker.C:
			r.mu.Lock()
			state := r.state
			r.mu.Unlock()
			if state == statePaused {
				continue
			}

			frame++
			buf := r.capture(frame)

			if state == stateSettling {
				r.evaluateSettle(buf)
			}
		}
	}
}

// capture writes one frame and returns its bytes (for change detection). The
// screenshot also returns the bytes, so no extra read is needed.
func (r *Recorder) capture(frame int) []byte {
	path := filepath.Join(r.framesDir, fmt.Sprintf("frame_%06d.png", frame))
	// Best-effort: never let a capture error kill the run. Explicitly viewport
	// only (not full page) so every frame is the same size — ffmpeg needs
	// constant dimensions.
	buf, _ := active().Screenshot(playwright.PageScreenshotOptions{
		Path:     playwright.String(path),
		FullPage: playwright.Bool(false),
	})
	return buf
}

// evaluateSettle decides, after capturing a settling frame, whether the page has
// settled enough to actually pause.
func (r *Recorder) evaluateSettle(buf []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != stateSettling {
		return // resumed (or already paused) in the meantime
	}

	now := time.Now()
	if buf != nil && !bytes.Equal(buf, r.lastFrame) {
		r.lastFrame = buf
		r.lastChange = now
	}

	if now.Sub(r.lastChange) >= settleQuiet || now.Sub(r.settleStart) >= settleMax {
		r.state = statePaused
		r.lastFrame = nil
	}
}

// Pause requests a pause but does not stop immediately: capture continues until
// the page has been visually unchanged for settleQuiet, or settleMax has elapsed
// since the request. It does not block — the settling runs on the recorder's
// goroutine. A Resume during settling cancels it and keeps recording.
// A nil receiver (recording disabled) is a no-op.
func (r *Recorder) Pause() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == statePaused {
		return
	}
	now := time.Now()
	r.state = stateSettling
	r.settleStart = now
	r.lastChange = now
	r.lastFrame = nil
}

// Resume restarts (or continues) capturing frames, cancelling any in-progress
// settling. A nil receiver (recording disabled) is a no-op.
func (r *Recorder) Resume() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.state = stateRecording
	r.lastFrame = nil
	r.mu.Unlock()
}

// Stop ends recording and returns the directory holding the captured frames.
func (r *Recorder) Stop() string {
	close(r.stop)
	<-r.done
	return r.framesDir
}
