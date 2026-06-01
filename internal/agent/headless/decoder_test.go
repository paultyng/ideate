package headless

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDecoder_Next_HappyPath(t *testing.T) {
	t.Parallel()
	r := &FakeRunner{Events: FakeEvents("Hello", " world", "!")}
	rc, err := r.Run(context.Background(), "p", Opts{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	defer func() { _ = rc.Close() }()
	d := NewDecoder(rc)
	var got []Event
	for {
		ev, err := d.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, ev)
	}
	want := []Event{
		{Kind: EventTextDelta, Delta: "Hello"},
		{Kind: EventTextDelta, Delta: " world"},
		{Kind: EventTextDelta, Delta: "!"},
		{Kind: EventDone},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), got)
	}
	for i := range got {
		if got[i].Kind != want[i].Kind || got[i].Delta != want[i].Delta {
			t.Errorf("event[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestDecoder_Next_HonorsCtxCancellation(t *testing.T) {
	t.Parallel()
	r := &FakeRunner{Events: FakeEvents("a", "b", "c")}
	rc, _ := r.Run(context.Background(), "p", Opts{})
	defer func() { _ = rc.Close() }()
	d := NewDecoder(rc)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Next(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("Next on cancelled ctx = %v, want context.Canceled", err)
	}
}

func TestDecoder_Next_BadJSON(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("{not json}\n"))
	_, err := d.Next(context.Background())
	if err == nil {
		t.Errorf("expected unmarshal error, got nil")
	}
}

func TestDecoder_Next_SkipsBlankLines(t *testing.T) {
	t.Parallel()
	d := NewDecoder(strings.NewReader("\n\n" + `{"kind":"done"}` + "\n"))
	ev, err := d.Next(context.Background())
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind != EventDone {
		t.Errorf("got %v, want done", ev.Kind)
	}
}

func TestDrainText_Accumulates(t *testing.T) {
	t.Parallel()
	r := &FakeRunner{Events: FakeEvents("Hey ", "Paul,", " ready", " to ", "code.")}
	rc, _ := r.Run(context.Background(), "p", Opts{})
	got, err := DrainText(context.Background(), rc)
	if err != nil {
		t.Fatalf("DrainText: %v", err)
	}
	if got != "Hey Paul, ready to code." {
		t.Errorf("DrainText = %q", got)
	}
}

func TestDrainText_StopsOnError(t *testing.T) {
	t.Parallel()
	r := &FakeRunner{Events: []Event{
		{Kind: EventTextDelta, Delta: "partial"},
		{Kind: EventError, Err: "rate_limit_exceeded"},
		{Kind: EventTextDelta, Delta: "shouldn't appear"},
	}}
	rc, _ := r.Run(context.Background(), "p", Opts{})
	got, err := DrainText(context.Background(), rc)
	if err == nil || !strings.Contains(err.Error(), "rate_limit_exceeded") {
		t.Errorf("err = %v, want one containing rate_limit_exceeded", err)
	}
	if got != "partial" {
		t.Errorf("got = %q, want %q", got, "partial")
	}
}

func TestDrainText_IgnoresNonText(t *testing.T) {
	t.Parallel()
	r := &FakeRunner{Events: []Event{
		{Kind: EventThinkingDelta, Delta: "internal monologue"},
		{Kind: EventTextDelta, Delta: "visible"},
		{Kind: EventToolUse, Tool: &ToolEvent{ID: "t1", Name: "Bash"}},
		{Kind: EventDone},
	}}
	rc, _ := r.Run(context.Background(), "p", Opts{})
	got, err := DrainText(context.Background(), rc)
	if err != nil {
		t.Fatalf("DrainText: %v", err)
	}
	if got != "visible" {
		t.Errorf("got %q, want %q", got, "visible")
	}
}
