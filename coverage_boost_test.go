package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-lynx/lynx-http/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ---------------------------------------------------------------------------
// validateConfigLocked – edge-case coverage
// ---------------------------------------------------------------------------

func TestValidateConfigLocked_NilConf(t *testing.T) {
	h := NewServiceHttp()
	h.conf = nil
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "configuration is nil")
}

func TestValidateConfigLocked_InvalidNetwork(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "udp", Addr: ":8080"}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid network protocol")
}

func TestValidateConfigLocked_UnixEmptyPath(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "unix", Addr: "   "}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "path must be non-empty")
}

func TestValidateConfigLocked_BadPort(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":99999"}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid port number")
}

func TestValidateConfigLocked_TimeoutZero(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		Timeout: &durationpb.Duration{Seconds: 0, Nanos: 0},
	}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout must be positive")
}

func TestValidateConfigLocked_TimeoutTooLarge(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		Timeout: &durationpb.Duration{Seconds: 400},
	}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timeout cannot exceed 5 minutes")
}

func TestValidateConfigLocked_NegativeIdleTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.idleTimeout = -1
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle timeout cannot be negative")
}

func TestValidateConfigLocked_IdleTimeoutTooLarge(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.idleTimeout = 700 * time.Second
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "idle timeout cannot exceed 10 minutes")
}

func TestValidateConfigLocked_NegativeMaxRequestSize(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.maxRequestSize = -1
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max request size cannot be negative")
}

func TestValidateConfigLocked_CircuitBreakerInvalidThreshold(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{
			Enabled:          true,
			MaxFailures:      5,
			FailureThreshold: 1.5, // > 1, invalid
		},
	}
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failure threshold must be between 0 and 1")
}

func TestValidateConfigLocked_NegativeShutdownTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.shutdownTimeout = -time.Second
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "shutdown timeout cannot be negative")
}

func TestValidateConfigLocked_KeepAliveTooLarge(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.keepAliveTimeout = 400 * time.Second
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "keep alive timeout cannot exceed 5 minutes")
}

func TestValidateConfigLocked_ReadHeaderTimeoutTooLarge(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.readHeaderTimeout = 120 * time.Second
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read header timeout cannot exceed 1 minute")
}

func TestValidateConfigLocked_NegativeMaxConnections(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.maxConnections = -1
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max connections cannot be negative")
}

// ---------------------------------------------------------------------------
// Configure
// ---------------------------------------------------------------------------

func TestConfigure_NilConfig(t *testing.T) {
	h := NewServiceHttp()
	err := h.Configure(nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be nil")
}

func TestConfigure_WrongType(t *testing.T) {
	h := NewServiceHttp()
	err := h.Configure("not-a-config")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid configuration type")
}

func TestConfigure_ValidConfig(t *testing.T) {
	h := NewServiceHttp()
	cfg := &conf.Http{Network: "tcp", Addr: ":9090", Timeout: &durationpb.Duration{Seconds: 10}}
	err := h.Configure(cfg)
	require.NoError(t, err)
	assert.Equal(t, cfg, h.conf)
}

func TestConfigure_InvalidConfigRollsBack(t *testing.T) {
	h := NewServiceHttp()
	original := &conf.Http{Network: "tcp", Addr: ":8080", Timeout: &durationpb.Duration{Seconds: 5}}
	h.conf = original
	h.initPerformanceDefaults()

	bad := &conf.Http{Network: "udp", Addr: "bad"} // invalid network
	err := h.Configure(bad)
	require.Error(t, err)
	// Original config should be restored
	assert.Equal(t, original, h.conf)
}

// ---------------------------------------------------------------------------
// setDefaultConfig branches
// ---------------------------------------------------------------------------

func TestSetDefaultConfig_Defaults(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{} // empty – all fields should be filled with defaults
	h.setDefaultConfig()
	assert.Equal(t, "tcp", h.conf.Network)
	assert.Equal(t, ":8080", h.conf.Addr)
	assert.NotNil(t, h.conf.Timeout)
	assert.NotNil(t, h.conf.Middleware)
	assert.NotNil(t, h.conf.CircuitBreaker)
	assert.NotNil(t, h.conf.Performance)
}

// ---------------------------------------------------------------------------
// initSecurityDefaults
// ---------------------------------------------------------------------------

func TestInitSecurityDefaults_RateLimitDisabled(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Security: &conf.SecurityConfig{
			RateLimit: &conf.RateLimitConfig{Enabled: false},
		},
	}
	h.initSecurityDefaults()
	assert.Nil(t, h.rateLimiter)
}

func TestInitSecurityDefaults_CustomRateLimit(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Security: &conf.SecurityConfig{
			RateLimit: &conf.RateLimitConfig{
				Enabled:       true,
				RatePerSecond: 500,
				BurstLimit:    1000,
			},
		},
	}
	h.initSecurityDefaults()
	require.NotNil(t, h.rateLimiter)
	assert.InEpsilon(t, float64(500), float64(h.rateLimiter.Limit()), 0.01)
	assert.Equal(t, 1000, h.rateLimiter.Burst())
}

// ---------------------------------------------------------------------------
// initGracefulShutdownDefaults
// ---------------------------------------------------------------------------

func TestInitGracefulShutdownDefaults_FromConfig(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		GracefulShutdown: &conf.GracefulShutdownConfig{
			ShutdownTimeout: &durationpb.Duration{Seconds: 15},
		},
	}
	h.initGracefulShutdownDefaults()
	assert.Equal(t, 15*time.Second, h.shutdownTimeout)
}

func TestInitGracefulShutdownDefaults_DefaultTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{}
	h.initGracefulShutdownDefaults()
	assert.Equal(t, 30*time.Second, h.shutdownTimeout)
}

// ---------------------------------------------------------------------------
// createShutdownContext
// ---------------------------------------------------------------------------

func TestCreateShutdownContext_UsesTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.shutdownTimeout = 5 * time.Second
	ctx, cancel := h.createShutdownContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	assert.True(t, ok)
	assert.True(t, time.Until(deadline) > 0 && time.Until(deadline) <= 5*time.Second)
}

func TestCreateShutdownContext_DefaultTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.shutdownTimeout = 0
	ctx, cancel := h.createShutdownContext(context.Background())
	defer cancel()
	_, ok := ctx.Deadline()
	assert.True(t, ok)
}

func TestCreateShutdownContext_NilParent(t *testing.T) {
	h := NewServiceHttp()
	h.shutdownTimeout = 5 * time.Second
	ctx, cancel := h.createShutdownContext(nil)
	defer cancel()
	_, ok := ctx.Deadline()
	assert.True(t, ok)
}

// ---------------------------------------------------------------------------
// cleanupWithContext – nil server path
// ---------------------------------------------------------------------------

func TestCleanupWithContext_NilServer(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.server = nil
	err := h.cleanupWithContext(context.Background())
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// startupWithContext – canceled context
// ---------------------------------------------------------------------------

func TestStartupWithContext_CanceledContext(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := h.startupWithContext(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context canceled")
}

func TestStartupWithContext_NilConf(t *testing.T) {
	h := NewServiceHttp()
	h.conf = nil
	err := h.startupWithContext(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// ---------------------------------------------------------------------------
// handlers – notFoundHandler / methodNotAllowedHandler / enhancedErrorEncoder
// ---------------------------------------------------------------------------

func TestNotFoundHandler(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.initMetrics()

	handler := h.notFoundHandler()
	req := httptest.NewRequest(http.MethodGet, "/not-here", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":404`)
}

func TestMethodNotAllowedHandler(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.initMetrics()

	handler := h.methodNotAllowedHandler()
	req := httptest.NewRequest(http.MethodGet, "/some-path", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":405`)
}

func TestEnhancedErrorEncoder_SystemFailure(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.initMetrics()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	// A plain error without Kratos code mapping defaults to 500 via defaultErrorCode
	h.enhancedErrorEncoder(rr, req, errors.New("internal"))
	assert.Equal(t, http.StatusInternalServerError, rr.Code)
	assert.Contains(t, rr.Body.String(), `"code":500`)
}

func TestEnhancedErrorEncoder_BusinessError(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.initMetrics()
	// Use a Kratos error with a non-500 code
	h.ErrorCodeMapper = func(se *kratoserrors.Error) int { return 404 }

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.enhancedErrorEncoder(rr, req, fmt.Errorf("not found"))
	assert.Equal(t, http.StatusOK, rr.Code) // business error → HTTP 200
	assert.Contains(t, rr.Body.String(), `"code":404`)
}

// ---------------------------------------------------------------------------
// defaultErrorCode
// ---------------------------------------------------------------------------

func TestDefaultErrorCode(t *testing.T) {
	tests := []struct {
		name string
		err  *kratoserrors.Error
		want int
	}{
		{"nil", nil, 500},
		{"zero code", kratoserrors.New(0, "", ""), 500},
		{"non-zero code", kratoserrors.New(404, "", ""), 404},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, defaultErrorCode(tc.err))
		})
	}
}

// ---------------------------------------------------------------------------
// responseBodyCodeFromError
// ---------------------------------------------------------------------------

func TestResponseBodyCodeFromError_NoMapper(t *testing.T) {
	h := NewServiceHttp()
	code := h.responseBodyCodeFromError(errors.New("some error"))
	// No mapper → falls through to defaultErrorCode → 500
	assert.Equal(t, 500, code)
}

func TestResponseBodyCodeFromError_WithMapper(t *testing.T) {
	h := NewServiceHttp()
	h.ErrorCodeMapper = func(se *kratoserrors.Error) int { return 42 }
	code := h.responseBodyCodeFromError(errors.New("some error"))
	assert.Equal(t, 42, code)
}

// ---------------------------------------------------------------------------
// shouldOmitSuccessData
// ---------------------------------------------------------------------------

func TestShouldOmitSuccessData(t *testing.T) {
	tests := []struct {
		name string
		data any
		want bool
	}{
		{"nil", nil, true},
		{"non-nil string", "hello", false},
		{"empty map", map[string]any{}, true},
		{"non-empty map", map[string]any{"k": "v"}, false},
		{"empty slice", []int{}, true},
		{"non-empty slice", []int{1}, false},
		{"nil pointer", (*int)(nil), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, shouldOmitSuccessData(tc.data))
		})
	}
}

// ---------------------------------------------------------------------------
// error_logging helpers
// ---------------------------------------------------------------------------

func TestErrorCodeForLog(t *testing.T) {
	assert.EqualValues(t, 200, errorCodeForLog(nil))
	assert.EqualValues(t, 500, errorCodeForLog(errors.New("boom")))
}

func TestRequestLogArgs_Stringer(t *testing.T) {
	type stringer struct{ s string }
	// Does not implement Stringer – falls to %+v path
	got := requestLogArgs(stringer{s: "hello"})
	assert.Contains(t, got, "hello")
}

func TestRootCause_SingleError(t *testing.T) {
	err := errors.New("base")
	assert.Equal(t, err, rootCause(err))
}

func TestRootCause_Wrapped(t *testing.T) {
	base := errors.New("base")
	wrapped := fmt.Errorf("wrap: %w", base)
	assert.Equal(t, base, rootCause(wrapped))
}

func TestRootCause_Nil(t *testing.T) {
	assert.Nil(t, rootCause(nil))
}

func TestErrorLogFields_NilErr(t *testing.T) {
	assert.Nil(t, errorLogFields(nil))
}

func TestErrorLogFields_WithErr(t *testing.T) {
	fields := errorLogFields(errors.New("oops"))
	assert.NotEmpty(t, fields)
}

// ---------------------------------------------------------------------------
// monitoring helpers
// ---------------------------------------------------------------------------

func TestClampUsage(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{-1.0, 0.0},
		{0.5, 0.5},
		{1.5, 1.0},
		{0.0, 0.0},
		{1.0, 1.0},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, clampUsage(tc.in), "clampUsage(%v)", tc.in)
	}
}

func TestUpdateConnectionPoolUsage(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		Monitoring: &conf.MonitoringConfig{
			EnableConnectionMetrics: true,
		},
	}
	h.initMetrics()
	h.maxConnections = 100

	// Should not panic
	h.UpdateConnectionPoolUsage(50, 100)
}

func TestGetConnectionPoolUsage_NoMax(t *testing.T) {
	h := NewServiceHttp()
	h.maxConnections = 0
	assert.Equal(t, 0.0, h.GetConnectionPoolUsage())
}

func TestGetConnectionPoolUsage_WithMax(t *testing.T) {
	h := NewServiceHttp()
	h.maxConnections = 10
	usage := h.GetConnectionPoolUsage()
	assert.GreaterOrEqual(t, usage, 0.0)
	assert.LessOrEqual(t, usage, 1.0)
}

func TestUpdateConnectionPoolMetricsOnce(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableConnectionMetrics: true},
	}
	h.initMetrics()
	h.maxConnections = 50
	// Should not panic
	h.updateConnectionPoolMetricsOnce("test-pool")
}

func TestCheckRuntimeHealth_NilServer(t *testing.T) {
	h := NewServiceHttp()
	h.server = nil
	err := h.CheckRuntimeHealth()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not initialized")
}

// ---------------------------------------------------------------------------
// monitoringSnapshot helpers
// ---------------------------------------------------------------------------

func TestMonitoringSnapshotFromConfig(t *testing.T) {
	cfg := &conf.MonitoringConfig{
		EnableMetrics:           true,
		EnableRequestLogging:    true,
		MetricsPath:             "/m",
		HealthPath:              "/h",
		EnableConnectionMetrics: true,
	}
	snap := monitoringSnapshotFromConfig(cfg)
	assert.Equal(t, "/m", snap.metricsPath)
	assert.Equal(t, "/h", snap.healthPath)
	assert.True(t, snap.enableMetrics)
}

func TestMonitoringSnapshotFromConfig_Nil(t *testing.T) {
	snap := monitoringSnapshotFromConfig(nil)
	assert.Equal(t, defaultMetricsPath, snap.metricsPath)
	assert.Equal(t, defaultHealthPath, snap.healthPath)
}

func TestMonitoringSnapshotFromConfig_EmptyPaths(t *testing.T) {
	cfg := &conf.MonitoringConfig{MetricsPath: "  ", HealthPath: ""}
	snap := monitoringSnapshotFromConfig(cfg)
	assert.Equal(t, defaultMetricsPath, snap.metricsPath)
	assert.Equal(t, defaultHealthPath, snap.healthPath)
}

func TestRefreshMonitoringSnapshotLocked_NilConf(t *testing.T) {
	h := NewServiceHttp()
	h.conf = nil
	h.refreshMonitoringSnapshotLocked() // should not panic
	snap := h.monitoringSnapshotOrDefault()
	assert.NotNil(t, snap)
}

func TestMonitoringConfigOrDefault(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Monitoring: &conf.MonitoringConfig{EnableMetrics: true, MetricsPath: "/metrics", HealthPath: "/health"},
	}
	h.refreshMonitoringSnapshotLocked()
	mc := h.monitoringConfigOrDefault()
	assert.True(t, mc.EnableMetrics)
	assert.Equal(t, "/metrics", mc.MetricsPath)
}

func TestMonitoringConfigOrDefaultLocked_NilConf(t *testing.T) {
	h := NewServiceHttp()
	h.conf = nil
	mc := h.monitoringConfigOrDefaultLocked()
	assert.True(t, mc.EnableMetrics) // returns default
}

func TestDefaultMonitoringConfig(t *testing.T) {
	mc := defaultMonitoringConfig()
	assert.True(t, mc.EnableMetrics)
	assert.Equal(t, defaultMetricsPath, mc.MetricsPath)
}

func TestDefaultMonitoringSnapshot(t *testing.T) {
	snap := defaultMonitoringSnapshot()
	assert.True(t, snap.enableMetrics)
	assert.Equal(t, defaultMetricsPath, snap.metricsPath)
}

// ---------------------------------------------------------------------------
// ensureSemaphores
// ---------------------------------------------------------------------------

func TestEnsureSemaphores(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.maxConnections = 10
	h.maxConcurrentRequests = 20
	h.ensureSemaphores()
	assert.Equal(t, 10, cap(h.connectionSem))
	assert.Equal(t, 20, cap(h.requestSem))
}

func TestEnsureSemaphores_ZeroLimits(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.maxConnections = 0
	h.maxConcurrentRequests = 0
	h.ensureSemaphores()
	assert.Nil(t, h.connectionSem)
	assert.Nil(t, h.requestSem)
}

// ---------------------------------------------------------------------------
// ensureCircuitBreaker
// ---------------------------------------------------------------------------

func TestEnsureCircuitBreaker_Disabled(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network:        "tcp",
		Addr:           ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{Enabled: false},
	}
	cb := h.ensureCircuitBreaker()
	assert.Nil(t, cb)
}

func TestEnsureCircuitBreaker_Enabled(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{
			Enabled:          true,
			MaxFailures:      3,
			MaxRequests:      5,
			FailureThreshold: 0.5,
			Timeout:          &durationpb.Duration{Seconds: 30},
		},
	}
	cb := h.ensureCircuitBreaker()
	require.NotNil(t, cb)
	assert.Equal(t, CircuitBreakerClosed, cb.GetState())
}

// ---------------------------------------------------------------------------
// resetOnce
// ---------------------------------------------------------------------------

func TestResetOnce(t *testing.T) {
	var called int
	h := NewServiceHttp()
	h.semInitOnce.Do(func() { called++ })
	assert.Equal(t, 1, called)

	resetOnce(&h.semInitOnce)
	h.semInitOnce.Do(func() { called++ })
	assert.Equal(t, 2, called)
}

// ---------------------------------------------------------------------------
// circuitBreakerMiddleware
// ---------------------------------------------------------------------------

func TestCircuitBreakerMiddleware_NilBreaker(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network:        "tcp",
		Addr:           ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{Enabled: false},
	}
	h.initMetrics()

	mw := h.circuitBreakerMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	reply, err := handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

func TestCircuitBreakerMiddleware_Success(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{
			Enabled:     true,
			MaxFailures: 5,
			MaxRequests: 3,
		},
	}
	h.initMetrics()

	mw := h.circuitBreakerMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	reply, err := handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

func TestCircuitBreakerMiddleware_RecordsFailure(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{
			Enabled:     true,
			MaxFailures: 1,
			MaxRequests: 1,
		},
	}
	h.initMetrics()
	// Pre-create the circuit breaker so the middleware picks it up
	h.ensureCircuitBreaker()

	mw := h.circuitBreakerMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return nil, errors.New("internal server error")
	})
	_, _ = handler(context.Background(), nil)
}

// ---------------------------------------------------------------------------
// GetCircuitBreakerStats
// ---------------------------------------------------------------------------

func TestGetCircuitBreakerStats_Nil(t *testing.T) {
	h := NewServiceHttp()
	h.circuitBreaker = nil
	stats := h.GetCircuitBreakerStats()
	enabled, _ := stats["enabled"].(bool)
	assert.False(t, enabled)
}

func TestGetCircuitBreakerStats_WithBreaker(t *testing.T) {
	h := NewServiceHttp()
	h.circuitBreaker = NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures: 5,
		Timeout:     60 * time.Second,
		MaxRequests: 10,
	})
	stats := h.GetCircuitBreakerStats()
	enabled, _ := stats["enabled"].(bool)
	assert.True(t, enabled)
	assert.Contains(t, stats, "state")
}

// ---------------------------------------------------------------------------
// connectionLimitMiddleware
// ---------------------------------------------------------------------------

func TestConnectionLimitMiddleware_Passes(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableConnectionMetrics: false},
	}
	h.maxConnections = 10
	h.maxConcurrentRequests = 10
	h.initMetrics()
	h.ensureSemaphores()

	mw := h.connectionLimitMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	reply, err := handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

func TestConnectionLimitMiddleware_ExceedsLimit(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableConnectionMetrics: false},
	}
	h.maxConnections = 1
	h.initMetrics()
	h.ensureSemaphores()

	// Fill the semaphore
	h.connectionSem <- struct{}{}

	mw := h.connectionLimitMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	_, err := handler(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent request limit exceeded")
}

// ---------------------------------------------------------------------------
// rateLimitMiddleware
// ---------------------------------------------------------------------------

func TestRateLimitMiddleware_NoLimiter(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.rateLimiter = nil
	h.initMetrics()

	mw := h.rateLimitMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	reply, err := handler(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", reply)
}

// ---------------------------------------------------------------------------
// IsContextAware / GetServer / GetServerType / RegisterMetricsGatherer
// ---------------------------------------------------------------------------

func TestIsContextAware(t *testing.T) {
	h := NewServiceHttp()
	assert.True(t, h.IsContextAware())
}

func TestGetServer_Nil(t *testing.T) {
	h := NewServiceHttp()
	assert.Nil(t, h.GetServer())
}

func TestGetServerType(t *testing.T) {
	h := NewServiceHttp()
	assert.Equal(t, "http", h.GetServerType())
}

func TestRegisterMetricsGatherer_Nil(t *testing.T) {
	h := NewServiceHttp()
	// Should not panic
	h.RegisterMetricsGatherer(nil)
}

// ---------------------------------------------------------------------------
// ServeHTTP / Handle adapter
// ---------------------------------------------------------------------------

func TestNetHTTPToKratosHandlerAdapter_ServeHTTP(t *testing.T) {
	var called bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	adapter := &netHTTPToKratosHandlerAdapter{handler: inner}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	adapter.ServeHTTP(rr, req)
	assert.True(t, called)
	assert.Equal(t, http.StatusOK, rr.Code)
}

// ---------------------------------------------------------------------------
// listenConfigSnapshot
// ---------------------------------------------------------------------------

func TestListenConfigSnapshot(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":9999"}
	network, addr := h.listenConfigSnapshot()
	assert.Equal(t, "tcp", network)
	assert.Equal(t, ":9999", addr)
}

func TestListenConfigSnapshot_NilConf(t *testing.T) {
	h := NewServiceHttp()
	h.conf = nil
	network, addr := h.listenConfigSnapshot()
	assert.Equal(t, "", network)
	assert.Equal(t, "", addr)
}

// ---------------------------------------------------------------------------
// recordErrorMetric nil-safety
// ---------------------------------------------------------------------------

func TestRecordErrorMetric_NilHandler(t *testing.T) {
	h := NewServiceHttp()
	h.errorCounter = nil
	// Should not panic
	h.recordErrorMetric("GET", "/path", "not_found")
}

// ---------------------------------------------------------------------------
// summarizePayload
// ---------------------------------------------------------------------------

func TestSummarizePayload_Nil(t *testing.T) {
	assert.Equal(t, "<nil>", summarizePayload(nil))
}

func TestSummarizePayload_NonNil(t *testing.T) {
	result := summarizePayload("hello")
	assert.Contains(t, result, "string")
}

// ---------------------------------------------------------------------------
// sanitizeHeaders
// ---------------------------------------------------------------------------

func TestSanitizeHeaders_Nil(t *testing.T) {
	result := sanitizeHeaders(nil)
	assert.Nil(t, result)
}

// ---------------------------------------------------------------------------
// stopMetricsLoop / setMetricsLifecycleContext / reconfigureMetricsLoop
// ---------------------------------------------------------------------------

func TestStopMetricsLoop_NilCancel(t *testing.T) {
	h := NewServiceHttp()
	h.metricsCancel = nil
	h.metricsRootCancel = nil
	// Should not panic
	h.stopMetricsLoop()
}

func TestSetMetricsLifecycleContext(t *testing.T) {
	h := NewServiceHttp()
	h.setMetricsLifecycleContext(context.Background())
	assert.NotNil(t, h.metricsRootCtx)
	assert.NotNil(t, h.metricsRootCancel)
	h.stopMetricsLoop()
}

func TestSetMetricsLifecycleContext_NilCtx(t *testing.T) {
	h := NewServiceHttp()
	h.setMetricsLifecycleContext(nil)
	assert.NotNil(t, h.metricsRootCtx)
	h.stopMetricsLoop()
}

func TestReconfigureMetricsLoop_NoMetrics(t *testing.T) {
	h := NewServiceHttp()
	h.connectionPoolUsage = nil
	// Should not panic
	h.reconfigureMetricsLoop()
}

// ---------------------------------------------------------------------------
// initMonitoringDefaults
// ---------------------------------------------------------------------------

func TestInitMonitoringDefaults_NilMonitoring(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Monitoring: nil}
	h.initMonitoringDefaults()
	require.NotNil(t, h.conf.Monitoring)
	assert.Equal(t, defaultMetricsPath, h.conf.Monitoring.MetricsPath)
}

func TestInitMonitoringDefaults_EmptyPaths(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{
		Monitoring: &conf.MonitoringConfig{MetricsPath: "", HealthPath: ""},
	}
	h.initMonitoringDefaults()
	assert.Equal(t, defaultMetricsPath, h.conf.Monitoring.MetricsPath)
	assert.Equal(t, defaultHealthPath, h.conf.Monitoring.HealthPath)
}

// ---------------------------------------------------------------------------
// initCircuitBreakerDefaults
// ---------------------------------------------------------------------------

func TestInitCircuitBreakerDefaults_NilCB(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{CircuitBreaker: nil}
	h.initCircuitBreakerDefaults()
	require.NotNil(t, h.conf.CircuitBreaker)
	assert.True(t, h.conf.CircuitBreaker.Enabled)
}

// ---------------------------------------------------------------------------
// installConnStateHook / applyTCPBufferSettings
// ---------------------------------------------------------------------------

func TestInstallConnStateHook_NilServer(t *testing.T) {
	h := NewServiceHttp()
	// Should not panic
	h.installConnStateHook(nil)
}

// ---------------------------------------------------------------------------
// StopContext
// ---------------------------------------------------------------------------

func TestStopContext_NilServer(t *testing.T) {
	h := NewServiceHttp()
	h.server = nil
	err := h.StopContext(context.Background(), h)
	require.NoError(t, err)
}
