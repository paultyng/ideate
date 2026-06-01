package headless

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Decoder reads NDJSON [Event] frames from a [Runner.Run] reader. It
// owns no goroutines and applies no buffering policy beyond bufio's
// default line buffer; backpressure flows through the underlying
// reader.
//
// Use [NewDecoder] to construct.
type Decoder struct {
	scanner *bufio.Scanner
}

// maxFrameBytes caps any single NDJSON line to 1 MiB. Larger frames
// almost certainly indicate a misconfigured adapter that's emitting
// the whole transcript on one line; failing fast is preferable to
// unbounded buffer growth.
const maxFrameBytes = 1 << 20

// NewDecoder wraps r in a Decoder. The decoder does not take
// ownership of r; the caller is responsible for closing it.
func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxFrameBytes)
	return &Decoder{scanner: s}
}

// Next pulls the next event. Returns [io.EOF] when the stream ends
// cleanly. Honors ctx cancellation between frames (mid-frame
// cancellation is the caller's responsibility — closing the
// underlying reader interrupts the bufio.Scanner's blocking read).
func (d *Decoder) Next(ctx context.Context) (Event, error) {
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return Event{}, fmt.Errorf("headless decoder scan: %w", err)
		}
		return Event{}, io.EOF
	}
	line := d.scanner.Bytes()
	if len(line) == 0 {
		return d.Next(ctx)
	}
	var ev Event
	if err := json.Unmarshal(line, &ev); err != nil {
		return Event{}, fmt.Errorf("headless decoder unmarshal: %w", err)
	}
	return ev, nil
}

// DrainText reads r to completion, concatenates every [EventTextDelta]
// into a single string, and returns it. The reader is closed when the
// function returns, regardless of outcome.
//
// Stops early with the runner's error if it emits [EventError].
//
// Suited for one-shot summarizer-style callers that want the final
// reply as a single string. Callers that need streaming should use
// [Decoder] directly.
func DrainText(ctx context.Context, r io.ReadCloser) (string, error) {
	defer func() { _ = r.Close() }()
	d := NewDecoder(r)
	var b strings.Builder
	for {
		ev, err := d.Next(ctx)
		if errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return b.String(), err
		}
		switch ev.Kind {
		case EventTextDelta:
			b.WriteString(ev.Delta)
		case EventError:
			return b.String(), fmt.Errorf("headless runner: %s", ev.Err)
		}
	}
}
