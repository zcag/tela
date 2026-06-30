package llm

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// instantCompleter returns its name immediately; calls counts invocations.
type instantCompleter struct {
	name  string
	calls int64
}

func (c *instantCompleter) Model() string { return c.name }
func (c *instantCompleter) Complete(_ context.Context, _, _ string) (string, error) {
	atomic.AddInt64(&c.calls, 1)
	return c.name, nil
}

// blockingCompleter blocks in Complete until release is closed, so a test can
// hold gate slots open.
type blockingCompleter struct {
	name    string
	calls   int64
	release chan struct{}
}

func (c *blockingCompleter) Model() string { return c.name }
func (c *blockingCompleter) Complete(ctx context.Context, _, _ string) (string, error) {
	atomic.AddInt64(&c.calls, 1)
	select {
	case <-c.release:
		return c.name, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// A saturated foreground gate spills the overflow request to the overflow client,
// while the in-gate requests stay on the primary.
func TestForegroundGateSpillsToOverflow(t *testing.T) {
	prim := &blockingCompleter{name: "primary", release: make(chan struct{})}
	over := &instantCompleter{name: "overflow"}
	s := &Service{cl: prim, overflow: over, sem: make(chan struct{}, 2), wait: 40 * time.Millisecond}
	var hookFires int64
	s.SetSpillRecorder(func() { atomic.AddInt64(&hookFires, 1) })

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = s.Complete(context.Background(), "s", "u") }()
	}
	waitFor(t, func() bool { return atomic.LoadInt64(&prim.calls) == 2 }) // both slots held
	if g := s.Stats(); g.Limit != 2 || g.InFlight != 2 || !g.Overflow {
		t.Fatalf("gate stats while full: %+v, want Limit=2 InFlight=2 Overflow=true", g)
	}

	out, err := s.Complete(context.Background(), "s", "u") // can't get a slot in time
	if err != nil {
		t.Fatalf("spill call errored: %v", err)
	}
	if out != "overflow" {
		t.Fatalf("want spill to overflow, got %q", out)
	}
	if got := atomic.LoadInt64(&over.calls); got != 1 {
		t.Fatalf("overflow calls = %d, want 1", got)
	}
	if s.Stats().Spills != 1 || atomic.LoadInt64(&hookFires) != 1 {
		t.Fatalf("spills=%d hookFires=%d, want 1/1", s.Stats().Spills, atomic.LoadInt64(&hookFires))
	}

	close(prim.release)
	wg.Wait()
}

// Background calls bypass the gate entirely — they use the primary even when the
// gate is full, and never touch the overflow layer.
func TestBackgroundBypassesGate(t *testing.T) {
	prim := &instantCompleter{name: "primary"}
	over := &instantCompleter{name: "overflow"}
	s := &Service{cl: prim, overflow: over, sem: make(chan struct{}, 1), wait: 10 * time.Millisecond}
	s.sem <- struct{}{} // occupy the only slot

	out, err := s.Complete(WithBackground(context.Background()), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if out != "primary" {
		t.Fatalf("background must use primary, got %q", out)
	}
	if atomic.LoadInt64(&over.calls) != 0 {
		t.Fatal("background must never spill to overflow")
	}
}

// With no overflow configured, a saturated gate degrades to the primary
// best-effort (queue-then-proceed) rather than failing the request.
func TestGateFullNoOverflowDegradesToPrimary(t *testing.T) {
	prim := &instantCompleter{name: "primary"}
	s := &Service{cl: prim, sem: make(chan struct{}, 1), wait: 10 * time.Millisecond}
	s.sem <- struct{}{} // occupy the only slot

	out, err := s.Complete(context.Background(), "s", "u")
	if err != nil {
		t.Fatal(err)
	}
	if out != "primary" {
		t.Fatalf("want degrade to primary, got %q", out)
	}
}
