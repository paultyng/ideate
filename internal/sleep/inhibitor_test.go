package sleep

import "testing"

func TestNoop(t *testing.T) {
	t.Parallel()
	n := Noop()
	if n.Held() {
		t.Error("Noop().Held() should be false")
	}
	n.Acquire("test")
	if n.Held() {
		t.Error("Noop().Acquire should not hold anything")
	}
	n.Release()
	if n.Held() {
		t.Error("Noop().Release left Held=true")
	}
}
