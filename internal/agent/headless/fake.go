package headless

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// FakeRunner implements [Runner] for tests. It returns a fixed set of
// [Event]s as NDJSON on each Run call. Concurrency-safe: every call
// gets its own reader over the same canned script.
type FakeRunner struct {
	// Events is the canned script played back on every Run call.
	// Each event is rendered as one JSON line.
	Events []Event

	// Err, if non-nil, is returned from Run before any reader is
	// produced. For mid-stream errors, emit an EventError frame in
	// Events instead.
	Err error

	// OnRun, if non-nil, is invoked with each Run call's prompt and
	// opts. Useful for assertions on what the caller passed.
	OnRun func(prompt string, opts Opts)
}

// Run satisfies [Runner].
func (f *FakeRunner) Run(_ context.Context, prompt string, opts Opts) (io.ReadCloser, error) {
	if f.OnRun != nil {
		f.OnRun(prompt, opts)
	}
	if f.Err != nil {
		return nil, f.Err
	}
	var b bytes.Buffer
	for _, ev := range f.Events {
		raw, err := json.Marshal(ev)
		if err != nil {
			return nil, err
		}
		b.Write(raw)
		b.WriteByte('\n')
	}
	return io.NopCloser(&b), nil
}

// FakeEvents builds a typical happy-path script: a sequence of text
// deltas (each becoming one EventTextDelta) followed by EventDone.
// Convenience for tests that don't need tool_use / thinking events.
func FakeEvents(textChunks ...string) []Event {
	out := make([]Event, 0, len(textChunks)+1)
	for _, c := range textChunks {
		out = append(out, Event{Kind: EventTextDelta, Delta: c})
	}
	out = append(out, Event{Kind: EventDone})
	return out
}

// JoinText is a small helper for tests that want to drain a FakeRunner
// reader without bringing in [DrainText]'s context plumbing.
func JoinText(r io.ReadCloser) (string, error) {
	defer func() { _ = r.Close() }()
	d := NewDecoder(r)
	var b strings.Builder
	for {
		ev, err := d.Next(context.Background())
		if errors.Is(err, io.EOF) {
			return b.String(), nil
		}
		if err != nil {
			return b.String(), err
		}
		if ev.Kind == EventTextDelta {
			b.WriteString(ev.Delta)
		}
	}
}
