package headless

import (
	"strings"
	"testing"
)

func TestCapBuffer_TruncatesAtCap(t *testing.T) {
	t.Parallel()
	b := &capBuffer{cap: 8}
	_, _ = b.Write([]byte("hello "))
	n, _ := b.Write([]byte("world!"))
	if n != 6 {
		t.Errorf("Write returned n=%d, want 6 (writer reports full input length)", n)
	}
	if b.String() != "hello wo" {
		t.Errorf("buf = %q, want %q", b.String(), "hello wo")
	}
}

func TestCapBuffer_DropsAfterFull(t *testing.T) {
	t.Parallel()
	b := &capBuffer{cap: 4}
	_, _ = b.Write([]byte("abcd"))
	_, _ = b.Write([]byte("efgh"))
	if b.String() != "abcd" {
		t.Errorf("buf = %q, want %q", b.String(), "abcd")
	}
}

func TestCapBuffer_ConcurrentWrites(t *testing.T) {
	t.Parallel()
	b := &capBuffer{cap: 1000}
	done := make(chan struct{})
	for range 10 {
		go func() {
			_, _ = b.Write([]byte(strings.Repeat("x", 50)))
			done <- struct{}{}
		}()
	}
	for range 10 {
		<-done
	}
	if got := b.String(); len(got) != 500 {
		t.Errorf("len = %d, want 500", len(got))
	}
}
