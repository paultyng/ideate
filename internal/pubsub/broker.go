// Package pubsub is an in-process generic fan-out broker. Each
// Subscribe call returns a buffered channel + a cancel func; Publish
// is non-blocking and drops the oldest queued event for any
// subscriber whose buffer is full (with a [slog.Warn] logged so a
// stuck subscriber surfaces in dogfood logs).
//
// Designed for single-process desktop apps where a small number of
// known publishers fans out to a small number of long-lived
// subscribers (App.Startup bridge, MCP review-signal waiters,
// future hook-event consumers). Not a distributed bus, not a
// message queue, not network IPC — wire-formatting, durability,
// and replay are explicit non-goals.
//
// Discipline (mirrors Crush's internal/pubsub):
//   - cancel() removes a subscription from the broker but does NOT
//     close its channel — that prevents a Publish-during-cancel race
//     from panicking on a closed channel. Use [Broker.Close] for
//     clean shutdown.
//   - Close() closes every subscriber channel exactly once and
//     marks the broker so subsequent Subscribe / Publish are no-ops.
//   - Publish iterates a snapshot of subscribers taken under a read
//     lock so it never blocks subscribers' cancels.
package pubsub

import (
	"log/slog"
	"sync"
)

// defaultBufferSize is the per-subscriber ring buffer used when no
// [WithBufferSize] option is supplied. Sized to absorb the long
// bursts the app's event bridge subscriber sees during compaction
// or when the watcher fires many idea:changed/repo:changed events
// in a tight window — wailsRuntime.EventsEmit serializes through a
// single JS bridge goroutine, so a slow webview frame can stall
// drains for tens of ms, plenty to overrun a 16-slot buffer.
//
// 256 absorbs ~150ms of typical event load at our publish rate
// without dropping. Pick a larger value via [WithBufferSize] for
// fan-in subscribers that legitimately need more headroom; pick a
// smaller one for narrow Filter() consumers where memory matters.
const defaultBufferSize = 256

// Event is the canonical payload for app-wide event-name + opaque-
// data fan-out. Used by [Broker[Event]] consumers — in particular
// the App's bridge goroutine that translates each Event into a
// `wailsRuntime.EventsEmit(Name, Data)` call. Lives here (next to
// [Broker]) so the publisher packages (mcp, hooks) can import a
// single thing rather than a separate event package; pubsub stays
// the only place that knows about both sides of the contract.
type Event struct {
	Name string
	Data any
}

// Broker is a generic fan-out pub/sub. Zero value is not usable —
// construct via [New].
type Broker[T any] struct {
	mu         sync.RWMutex
	subs       map[*subscription[T]]struct{}
	bufferSize int
	closed     bool
}

// subscription couples a subscriber's receive channel with the
// snapshot identity used to remove it on cancel.
type subscription[T any] struct {
	ch chan T
}

// Option configures a [Broker] at construction.
type Option func(*config)

type config struct {
	bufferSize int
}

// WithBufferSize overrides the per-subscriber ring buffer size.
// Must be > 0; values <= 0 fall back to [defaultBufferSize].
func WithBufferSize(n int) Option {
	return func(c *config) {
		if n > 0 {
			c.bufferSize = n
		}
	}
}

// New constructs a [Broker] for events of type T.
func New[T any](opts ...Option) *Broker[T] {
	c := config{bufferSize: defaultBufferSize}
	for _, opt := range opts {
		opt(&c)
	}
	return &Broker[T]{
		subs:       make(map[*subscription[T]]struct{}),
		bufferSize: c.bufferSize,
	}
}

// Subscribe registers a new subscriber and returns its receive
// channel plus a cancel func. The channel has the broker's per-
// subscriber buffer size. The returned cancel func removes the
// subscription from the broker; it is idempotent and may be called
// from any goroutine. cancel does NOT close the channel — use
// [Broker.Close] for that. Subscribe returns a closed channel and a
// no-op cancel if the broker is already closed.
func (b *Broker[T]) Subscribe() (<-chan T, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		dead := make(chan T)
		close(dead)
		return dead, func() {}
	}

	sub := &subscription[T]{ch: make(chan T, b.bufferSize)}
	b.subs[sub] = struct{}{}

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, sub)
			b.mu.Unlock()
		})
	}
	return sub.ch, cancel
}

// Publish delivers event to every active subscriber. It never
// blocks: if a subscriber's channel is full, the oldest queued
// event is evicted (logged via [slog.Warn]) and the new event takes
// its slot. Subscribers cancelled mid-publish may or may not
// receive the event depending on iteration order — this is
// intentional and matches the snapshot-then-iterate semantics. No-op
// after [Broker.Close].
func (b *Broker[T]) Publish(event T) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}
	// Snapshot under the lock so subscribers can cancel concurrently
	// with iteration without us holding the broker lock during sends.
	subs := make([]*subscription[T], 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.mu.RUnlock()

	for _, s := range subs {
		select {
		case s.ch <- event:
		default:
			// Buffer full — drop oldest, then send. The drain may
			// race with a fast receiver (no-op then) which is
			// fine: the new event still lands.
			select {
			case <-s.ch:
				slog.Warn("pubsub: dropped oldest event due to slow subscriber",
					slog.Int("buffer", b.bufferSize))
			default:
			}
			select {
			case s.ch <- event:
			default:
				// Shouldn't reach here under normal contention; if
				// we do, multiple publishers raced for the same
				// drained slot. Drop the new event and warn.
				slog.Warn("pubsub: dropped new event after drain race")
			}
		}
	}
}

// Close marks the broker as closed and closes every active
// subscriber's channel. Idempotent. Subsequent Publish is a no-op
// and Subscribe returns a closed channel + no-op cancel.
func (b *Broker[T]) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for s := range b.subs {
		close(s.ch)
	}
	b.subs = nil
}

// Filter returns a derived subscription that delivers only events
// for which pred returns true. The returned cancel func tears down
// the filter goroutine and unsubscribes from the underlying broker.
// Used by callsites that previously open-coded a per-key channel
// map (e.g. waiting on a specific review ID).
func Filter[T any](b *Broker[T], pred func(T) bool) (<-chan T, func()) {
	src, srcCancel := b.Subscribe()
	out := make(chan T, cap(src))
	done := make(chan struct{})

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			close(done)
			srcCancel()
		})
	}

	go func() {
		defer close(out)
		for {
			select {
			case ev, ok := <-src:
				if !ok {
					return
				}
				if !pred(ev) {
					continue
				}
				select {
				case out <- ev:
				case <-done:
					return
				}
			case <-done:
				return
			}
		}
	}()

	return out, cancel
}
