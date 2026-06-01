package pubsub

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// recvWithin pulls an event off ch with a deadline so a stuck test
// fails fast instead of hanging the whole package.
func recvWithin[T any](t *testing.T, ch <-chan T, d time.Duration) (T, bool) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		return ev, ok
	case <-time.After(d):
		var zero T
		return zero, false
	}
}

func TestBroker_PublishSubscribe(t *testing.T) {
	t.Parallel()
	b := New[int]()

	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(7)
	got, ok := recvWithin(t, ch, time.Second)
	if !ok || got != 7 {
		t.Fatalf("got %v, ok=%v; want 7, ok=true", got, ok)
	}
}

func TestBroker_FanOut(t *testing.T) {
	t.Parallel()
	b := New[string]()

	ch1, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	defer cancel1()
	defer cancel2()

	b.Publish("hello")

	for _, ch := range []<-chan string{ch1, ch2} {
		got, ok := recvWithin(t, ch, time.Second)
		if !ok || got != "hello" {
			t.Errorf("subscriber missed event: got %q ok=%v", got, ok)
		}
	}
}

func TestBroker_Cancel_RemovesSubscriber(t *testing.T) {
	t.Parallel()
	b := New[int]()

	ch1, cancel1 := b.Subscribe()
	ch2, cancel2 := b.Subscribe()
	defer cancel2()

	cancel1()

	b.Publish(42)

	// ch1 should NOT receive (cancelled). ch2 should.
	if _, ok := recvWithin(t, ch1, 100*time.Millisecond); ok {
		t.Error("cancelled subscriber received an event")
	}
	if got, ok := recvWithin(t, ch2, time.Second); !ok || got != 42 {
		t.Errorf("active subscriber: got %v ok=%v", got, ok)
	}
}

func TestBroker_Cancel_Idempotent(t *testing.T) {
	t.Parallel()
	b := New[int]()
	_, cancel := b.Subscribe()

	// Multiple cancels must not panic, must not deadlock.
	cancel()
	cancel()
	cancel()
}

func TestBroker_Close_ClosesAllSubscribers(t *testing.T) {
	t.Parallel()
	b := New[int]()

	ch1, _ := b.Subscribe()
	ch2, _ := b.Subscribe()

	b.Close()

	for i, ch := range []<-chan int{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("subscriber %d: expected closed channel, got open value", i)
			}
		case <-time.After(time.Second):
			t.Errorf("subscriber %d: channel did not close after Broker.Close", i)
		}
	}
}

func TestBroker_Close_Idempotent(t *testing.T) {
	t.Parallel()
	b := New[int]()
	_, _ = b.Subscribe()

	b.Close()
	b.Close()
	b.Close()
}

func TestBroker_PublishAfterClose_NoOp(t *testing.T) {
	t.Parallel()
	b := New[int]()
	b.Close()
	// Must not panic.
	b.Publish(1)
}

func TestBroker_SubscribeAfterClose_ReturnsClosedChannel(t *testing.T) {
	t.Parallel()
	b := New[int]()
	b.Close()

	ch, cancel := b.Subscribe()
	defer cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected closed channel from Subscribe-after-close")
		}
	case <-time.After(time.Second):
		t.Error("Subscribe-after-close did not return a closed channel")
	}
}

func TestBroker_SlowSubscriber_DropsOldest(t *testing.T) {
	t.Parallel()
	b := New[int](WithBufferSize(2))

	ch, cancel := b.Subscribe()
	defer cancel()

	// Fill the buffer + overflow without anyone reading.
	for i := range 5 {
		b.Publish(i)
	}

	// Read what's left in the channel. With drop-oldest semantics
	// the latest events should win — so the final value seen
	// should be 4 (the most recent publish).
	var last int
	for i := range 2 {
		got, ok := recvWithin(t, ch, time.Second)
		if !ok {
			t.Fatalf("recv %d: timed out", i)
		}
		last = got
	}
	if last != 4 {
		t.Errorf("last received = %d, want 4 (drop-oldest should preserve newest)", last)
	}
}

func TestBroker_BufferSize_Default(t *testing.T) {
	t.Parallel()
	b := New[int]()
	if b.bufferSize != defaultBufferSize {
		t.Errorf("default bufferSize = %d, want %d", b.bufferSize, defaultBufferSize)
	}
}

func TestBroker_BufferSize_Override(t *testing.T) {
	t.Parallel()
	b := New[int](WithBufferSize(64))
	if b.bufferSize != 64 {
		t.Errorf("bufferSize = %d, want 64", b.bufferSize)
	}
}

func TestBroker_BufferSize_InvalidIgnored(t *testing.T) {
	t.Parallel()
	for _, n := range []int{0, -1, -100} {
		b := New[int](WithBufferSize(n))
		if b.bufferSize != defaultBufferSize {
			t.Errorf("bufferSize(%d) = %d, want default %d", n, b.bufferSize, defaultBufferSize)
		}
	}
}

func TestBroker_ConcurrentPublishersAndSubscribers(t *testing.T) {
	t.Parallel()
	b := New[int](WithBufferSize(64))

	const subs = 4
	const pubs = 4
	const each = 50

	var wg sync.WaitGroup
	var totalReceived atomic.Int64
	cancels := make([]func(), 0, subs)

	for range subs {
		ch, cancel := b.Subscribe()
		cancels = append(cancels, cancel)
		wg.Go(func() {
			for range ch {
				totalReceived.Add(1)
			}
		})
	}

	var pubWG sync.WaitGroup
	for range pubs {
		pubWG.Go(func() {
			for i := range each {
				b.Publish(i)
			}
		})
	}
	pubWG.Wait()

	// Give the publisher goroutines time to drain into subscriber
	// channels before close.
	time.Sleep(50 * time.Millisecond)
	b.Close()
	wg.Wait()

	// We can't assert exact counts because slow subscribers may
	// drop, but we need at least *some* events to have landed —
	// the test's value is "no panic, no deadlock under contention".
	if totalReceived.Load() == 0 {
		t.Error("no events received under concurrent load")
	}
	for _, c := range cancels {
		c() // post-close cancels must be no-ops
	}
}

func TestFilter_PassesMatchingEvents(t *testing.T) {
	t.Parallel()
	b := New[int]()

	even, cancel := Filter(b, func(n int) bool { return n%2 == 0 })
	defer cancel()

	for _, n := range []int{1, 2, 3, 4, 5} {
		b.Publish(n)
	}

	// We expect 2 and 4. Pull until the filter goroutine has
	// processed all five inputs (drain via short timeouts).
	var got []int
	for {
		ev, ok := recvWithin(t, even, 200*time.Millisecond)
		if !ok {
			break
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0] != 2 || got[1] != 4 {
		t.Errorf("filter received %v, want [2 4]", got)
	}
}

func TestFilter_CancelStopsGoroutine(t *testing.T) {
	t.Parallel()
	b := New[int]()
	out, cancel := Filter(b, func(int) bool { return true })

	cancel()

	// out should close after cancel (the Filter goroutine returns
	// and the deferred close fires).
	select {
	case _, ok := <-out:
		if ok {
			t.Error("expected closed channel after Filter cancel")
		}
	case <-time.After(time.Second):
		t.Error("Filter goroutine did not exit on cancel")
	}
}

func TestFilter_CloseUpstream_ClosesDownstream(t *testing.T) {
	t.Parallel()
	b := New[int]()
	out, cancel := Filter(b, func(int) bool { return true })
	defer cancel()

	b.Close()

	select {
	case _, ok := <-out:
		if ok {
			t.Error("expected closed downstream channel after broker.Close")
		}
	case <-time.After(time.Second):
		t.Error("downstream channel did not close after broker.Close")
	}
}
