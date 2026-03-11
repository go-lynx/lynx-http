package http

import (
	"testing"
	"time"
)

func TestCircuitBreaker_HalfOpenCountsOnlyAdmittedRequests(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures: 1,
		Timeout:     5 * time.Millisecond,
		MaxRequests: 2,
	})

	closedGuard := cb.Allow()
	if !closedGuard.Allowed() {
		t.Fatal("expected closed-state request to be allowed")
	}
	cb.RecordFailure(closedGuard)

	if state := cb.GetState(); state != CircuitBreakerOpen {
		t.Fatalf("expected circuit to be open after failure, got %v", state)
	}

	time.Sleep(10 * time.Millisecond)

	probe1 := cb.Allow()
	if !probe1.Allowed() {
		t.Fatal("expected first half-open probe to be allowed")
	}
	probe2 := cb.Allow()
	if !probe2.Allowed() {
		t.Fatal("expected second half-open probe to be allowed")
	}
	blocked := cb.Allow()
	if blocked.Allowed() {
		t.Fatal("expected third half-open request to be blocked")
	}

	failures, requests, successes, state := cb.GetStats()
	if requests != 2 {
		t.Fatalf("expected 2 admitted half-open requests, got %d", requests)
	}
	if successes != 0 || failures != 1 {
		t.Fatalf("unexpected stats before recording results: failures=%d requests=%d successes=%d", failures, requests, successes)
	}
	if state != CircuitBreakerHalfOpen {
		t.Fatalf("expected half-open state, got %v", state)
	}

	cb.RecordSuccess(probe1)
	cb.RecordSuccess(probe2)
	cb.RecordFailure(blocked) // blocked requests must not affect breaker stats

	failures, requests, successes, state = cb.GetStats()
	if state != CircuitBreakerClosed {
		t.Fatalf("expected circuit to close after successful probes, got %v", state)
	}
	if failures != 0 || requests != 0 || successes != 0 {
		t.Fatalf("expected stats reset after closing, got failures=%d requests=%d successes=%d", failures, requests, successes)
	}
}

func TestCircuitBreaker_FailureThresholdPreventsEarlyOpening(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		FailureThreshold: 0.5,
		Timeout:          time.Hour,
		MaxRequests:      1,
	})

	// 97 successes, then 3 failures → rate = 3/100 = 3%, well below 50%
	for i := 0; i < 97; i++ {
		g := cb.Allow()
		cb.RecordSuccess(g)
	}
	for i := 0; i < 3; i++ {
		g := cb.Allow()
		cb.RecordFailure(g)
	}

	if state := cb.GetState(); state != CircuitBreakerClosed {
		t.Fatalf("expected circuit to stay closed with low failure rate, got %v", state)
	}
}

func TestCircuitBreaker_FailureThresholdOpensOnHighRate(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		FailureThreshold: 0.5,
		Timeout:          time.Hour,
		MaxRequests:      1,
	})

	// 3 successes then 3 failures → rate = 3/6 = 50%, meets threshold
	for i := 0; i < 3; i++ {
		g := cb.Allow()
		cb.RecordSuccess(g)
	}
	for i := 0; i < 3; i++ {
		g := cb.Allow()
		cb.RecordFailure(g)
	}

	if state := cb.GetState(); state != CircuitBreakerOpen {
		t.Fatalf("expected circuit to open with high failure rate, got %v", state)
	}
}

func TestCircuitBreaker_WindowDecay(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      5,
		FailureThreshold: 0.5,
		Timeout:          time.Hour,
		MaxRequests:      1,
	})

	// Accumulate 4 failures early
	for i := 0; i < 4; i++ {
		g := cb.Allow()
		cb.RecordFailure(g)
	}

	// Send many successes to trigger multiple decay cycles; old failures
	// should be halved away so the 5th failure does not open the circuit.
	for i := 0; i < 200; i++ {
		g := cb.Allow()
		cb.RecordSuccess(g)
	}

	g := cb.Allow()
	cb.RecordFailure(g)

	if state := cb.GetState(); state != CircuitBreakerClosed {
		f, _, _, _ := cb.GetStats()
		t.Fatalf("expected circuit to stay closed after decay (failures=%d), got state %v", f, state)
	}
}

func TestCircuitBreaker_WindowResetsOnRecovery(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      1,
		FailureThreshold: 0.5,
		Timeout:          5 * time.Millisecond,
		MaxRequests:      1,
	})

	// Open the circuit
	g := cb.Allow()
	cb.RecordFailure(g)
	if state := cb.GetState(); state != CircuitBreakerOpen {
		t.Fatalf("expected open, got %v", state)
	}

	// Wait for timeout, probe succeeds → closes
	time.Sleep(10 * time.Millisecond)
	probe := cb.Allow()
	cb.RecordSuccess(probe)
	if state := cb.GetState(); state != CircuitBreakerClosed {
		t.Fatalf("expected closed after recovery, got %v", state)
	}

	if wr := cb.GetWindowRequests(); wr != 0 {
		t.Fatalf("expected window requests to be 0 after recovery, got %d", wr)
	}
}

func TestCircuitBreaker_ZeroThresholdUsesAbsoluteCount(t *testing.T) {
	// FailureThreshold defaults to 0.5 in NewCircuitBreaker when 0 is passed.
	// Explicitly pass a very small threshold to approximate "pure MaxFailures" mode.
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		FailureThreshold: 0.001,
		Timeout:          time.Hour,
		MaxRequests:      1,
	})

	// 100 successes then 3 failures. Rate = 3/103 ≈ 2.9% but threshold is 0.1%
	for i := 0; i < 100; i++ {
		g := cb.Allow()
		cb.RecordSuccess(g)
	}
	for i := 0; i < 3; i++ {
		g := cb.Allow()
		cb.RecordFailure(g)
	}

	if state := cb.GetState(); state != CircuitBreakerOpen {
		t.Fatalf("expected circuit to open with very low threshold, got %v", state)
	}
}

func TestCircuitBreaker_BlockedRequestDoesNotMutateOpenState(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures: 1,
		Timeout:     time.Hour,
		MaxRequests: 1,
	})

	guard := cb.Allow()
	cb.RecordFailure(guard)

	if state := cb.GetState(); state != CircuitBreakerOpen {
		t.Fatalf("expected circuit open, got %v", state)
	}

	blocked := cb.Allow()
	if blocked.Allowed() {
		t.Fatal("expected request to be blocked while circuit is open")
	}
	cb.RecordSuccess(blocked)
	cb.RecordFailure(blocked)

	failures, requests, successes, state := cb.GetStats()
	if failures != 1 || requests != 0 || successes != 0 || state != CircuitBreakerOpen {
		t.Fatalf("blocked request should not change stats; got failures=%d requests=%d successes=%d state=%v", failures, requests, successes, state)
	}
}
