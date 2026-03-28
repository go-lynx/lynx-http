// Package http implements the HTTP server plugin for the Lynx framework.
package http

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-lynx/lynx/log"
)

// CircuitBreakerState represents the state of a circuit breaker
type CircuitBreakerState int

const (
	// CircuitBreakerClosed - normal operation
	CircuitBreakerClosed CircuitBreakerState = iota
	// CircuitBreakerOpen - circuit is open, requests are rejected
	CircuitBreakerOpen
	// CircuitBreakerHalfOpen - testing if service has recovered
	CircuitBreakerHalfOpen
)

// CircuitBreakerConfig holds configuration for circuit breaker
type CircuitBreakerConfig struct {
	// MaxFailures is the maximum number of failures before opening the circuit
	MaxFailures int32
	// Timeout is how long to wait before attempting to close the circuit
	Timeout time.Duration
	// MaxRequests is the maximum number of requests allowed in half-open state
	MaxRequests int32
	// FailureThreshold is the failure rate threshold (0.0 to 1.0)
	FailureThreshold float64
}

// CircuitBreaker implements a circuit breaker pattern
type CircuitBreaker struct {
	config       CircuitBreakerConfig
	state        CircuitBreakerState
	failures     int32
	requests     int32
	successes    int32
	lastFailTime time.Time
	mutex        sync.RWMutex

	// windowRequests tracks total admitted requests in the current closed-state
	// measurement window. Together with failures it computes the failure rate
	// checked against FailureThreshold. The window auto-decays (halves) when it
	// exceeds a cap derived from MaxFailures, so stale history does not
	// permanently dilute the rate.
	windowRequests int32
}

// RequestGuard captures whether a request was admitted by the circuit breaker.
// It allows callers to record success/failure only for requests that were
// actually counted by the breaker state machine.
type RequestGuard struct {
	allowed bool
}

// NewCircuitBreaker creates a new circuit breaker
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	// Set defaults if not provided
	if config.MaxFailures == 0 {
		config.MaxFailures = 5
	}
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if config.MaxRequests == 0 {
		config.MaxRequests = 10
	}
	if config.FailureThreshold == 0 {
		config.FailureThreshold = 0.5
	}

	return &CircuitBreaker{
		config: config,
		state:  CircuitBreakerClosed,
	}
}

// Allow checks if a request should be allowed through.
// The returned guard records whether the request was admitted by the breaker
// and therefore whether the caller should report success/failure back.
func (cb *CircuitBreaker) Allow() RequestGuard {
	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	switch cb.state {
	case CircuitBreakerClosed:
		cb.windowRequests++
		// Auto-decay: halve counters when the window exceeds a cap so that
		// stale successes/failures don't permanently dilute the failure rate.
		wCap := cb.config.MaxFailures * 4
		if wCap < 20 {
			wCap = 20
		}
		if wCap > 1000 {
			wCap = 1000
		}
		if cb.windowRequests > wCap {
			cb.windowRequests /= 2
			cb.failures /= 2
		}
		return RequestGuard{allowed: true}
	case CircuitBreakerOpen:
		// Check if timeout has passed.
		if time.Since(cb.lastFailTime) > cb.config.Timeout {
			cb.state = CircuitBreakerHalfOpen
			cb.requests = 0
			cb.successes = 0
			cb.requests++ // admit and count the probe request immediately
			log.Infof("Circuit breaker transitioning to half-open state")
			return RequestGuard{allowed: true}
		}
		return RequestGuard{allowed: false}
	case CircuitBreakerHalfOpen:
		if cb.requests >= cb.config.MaxRequests {
			return RequestGuard{allowed: false}
		}
		cb.requests++
		return RequestGuard{allowed: true}
	default:
		return RequestGuard{allowed: false}
	}
}

// Allowed reports whether the associated request was admitted by the breaker.
func (g RequestGuard) Allowed() bool {
	return g.allowed
}

// RecordSuccess records a successful admitted request.
func (cb *CircuitBreaker) RecordSuccess(guard RequestGuard) {
	if !guard.Allowed() {
		return
	}

	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.successes++

	if cb.state == CircuitBreakerHalfOpen {
		if cb.successes >= cb.config.MaxRequests {
			cb.state = CircuitBreakerClosed
			cb.failures = 0
			cb.requests = 0
			cb.successes = 0
			cb.windowRequests = 0
			log.Infof("Circuit breaker closed - service recovered")
		}
	}
}

// RecordFailure records a failed admitted request.
func (cb *CircuitBreaker) RecordFailure(guard RequestGuard) {
	if !guard.Allowed() {
		return
	}

	cb.mutex.Lock()
	defer cb.mutex.Unlock()

	cb.failures++
	cb.lastFailTime = time.Now()

	switch cb.state {
	case CircuitBreakerClosed:
		if cb.failures >= cb.config.MaxFailures {
			shouldOpen := true
			// When FailureThreshold is configured, also require the failure
			// rate to exceed the threshold. This prevents opening on a small
			// absolute number of failures scattered across many requests.
			if cb.config.FailureThreshold > 0 && cb.windowRequests > 0 {
				rate := float64(cb.failures) / float64(cb.windowRequests)
				shouldOpen = rate >= cb.config.FailureThreshold
			}
			if shouldOpen {
				var ratePct float64
				if cb.windowRequests > 0 {
					ratePct = float64(cb.failures) / float64(cb.windowRequests) * 100
				}
				cb.state = CircuitBreakerOpen
				log.Warnf("Circuit breaker opened - failures: %d/%d requests (%.1f%% failure rate)",
					cb.failures, cb.windowRequests, ratePct)
			}
		}
	case CircuitBreakerHalfOpen:
		// Return to open state.
		cb.state = CircuitBreakerOpen
		log.Warnf("Circuit breaker returned to open state - failure in half-open")
	default:
	}
}

// GetState returns the current state of the circuit breaker
func (cb *CircuitBreaker) GetState() CircuitBreakerState {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.state
}

// GetStats returns current statistics
func (cb *CircuitBreaker) GetStats() (int32, int32, int32, CircuitBreakerState) {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.failures, cb.requests, cb.successes, cb.state
}

// GetWindowRequests returns the closed-state sliding window request count.
func (cb *CircuitBreaker) GetWindowRequests() int32 {
	cb.mutex.RLock()
	defer cb.mutex.RUnlock()
	return cb.windowRequests
}

// circuitBreakerMiddleware creates a circuit breaker middleware for HTTP requests
func (h *ServiceHttp) circuitBreakerMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			cb := h.ensureCircuitBreaker()
			if cb == nil {
				return handler(ctx, req)
			}

			// Check if request should be allowed
			guard := cb.Allow()
			if !guard.Allowed() {
				// Circuit is open, reject request
				method, path := requestMetadata(ctx)

				// Record circuit breaker rejection
				h.recordErrorMetric(method, path, "circuit_breaker_open")

				return nil, fmt.Errorf("circuit breaker is open - service unavailable")
			}

			// Execute the request
			reply, err = handler(ctx, req)

			// Record result only for requests that were admitted by the breaker.
			if err != nil {
				cb.RecordFailure(guard)
			} else {
				cb.RecordSuccess(guard)
			}

			return reply, err
		}
	}
}

// GetCircuitBreakerStats returns circuit breaker statistics
func (h *ServiceHttp) GetCircuitBreakerStats() map[string]interface{} {
	if h.circuitBreaker == nil {
		return map[string]interface{}{
			"enabled": false,
		}
	}

	failures, requests, successes, state := h.circuitBreaker.GetStats()

	return map[string]interface{}{
		"enabled":         true,
		"state":           state,
		"failures":        failures,
		"requests":        requests,
		"successes":       successes,
		"window_requests": h.circuitBreaker.GetWindowRequests(),
	}
}
