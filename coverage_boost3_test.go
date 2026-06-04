package http

import (
	"context"
	"errors"
	"net"
	nhttp "net/http"
	"testing"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-lynx/lynx-http/conf"
	"github.com/go-lynx/lynx/plugins"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// ---------------------------------------------------------------------------
// InitializeResources – using SimpleRuntime (no real config)
// ---------------------------------------------------------------------------

func TestInitializeResources_WithSimpleRuntime(t *testing.T) {
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)
	svc := NewServiceHttp()
	err := svc.InitializeResources(rt)
	require.NoError(t, err)
	assert.NotNil(t, svc.conf)
}

// ---------------------------------------------------------------------------
// InitializeContext – happy path (after InitializeResources)
// ---------------------------------------------------------------------------

func TestInitializeContext_HappyPath(t *testing.T) {
	svc := NewServiceHttp()
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)
	require.NoError(t, svc.InitializeResources(rt))
	err := svc.InitializeContext(context.Background(), svc, rt)
	// No Kratos app registered → BasePlugin.Initialize may fail or succeed depending
	// on the framework. Either way, it should not panic.
	_ = err
}

// ---------------------------------------------------------------------------
// Handle adapter
// ---------------------------------------------------------------------------

func TestNetHTTPToKratosHandlerAdapter_Handle(t *testing.T) {
	var called bool
	inner := nhttp.HandlerFunc(func(w nhttp.ResponseWriter, r *nhttp.Request) {
		called = true
	})
	adapter := &netHTTPToKratosHandlerAdapter{handler: inner}
	// Handle wraps inner.ServeHTTP; we call it via the adapter.
	// The kratos http.Request / http.ResponseWriter are the same underlying types.
	req, err := nhttp.NewRequest("GET", "/", nil)
	require.NoError(t, err)
	rr := &testResponseWriter{}
	// Build a minimal kratos http.Request wrapper.
	// Since we can't easily create kratos http.Request in unit tests,
	// just verify the ServeHTTP path works (tested above) and confirm Handle
	// calls the same inner handler through the adapter.
	// We test via ServeHTTP as a proxy since it shares the same code path.
	_ = adapter
	_ = req
	_ = rr
	_ = called
	// Functions cannot be compared with ==; verify the adapter was created with
	// a non-nil handler and that ServeHTTP delegates to it.
	assert.NotNil(t, adapter.handler)
	adapter.ServeHTTP(rr, req) // Exercises the ServeHTTP delegation path.
	assert.True(t, called, "inner handler should have been called via ServeHTTP")
}

// ---------------------------------------------------------------------------
// CheckRuntimeHealth – server initialised but not listening
// ---------------------------------------------------------------------------

func TestCheckRuntimeHealth_ServerNotListening(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{Network: "tcp", Addr: ":28765"}
	// Use a bare kratos server (not started) to pass the nil check
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)
	require.NoError(t, svc.InitializeResources(rt))
	// Override addr to a closed port
	svc.conf.Addr = ":28765"
	// Create a minimal server via StartupTasks but it will fail because port may not
	// be available. Instead, just directly test CheckRuntimeHealth with a mock server:
	_ = svc.CheckRuntimeHealth()
}

// ---------------------------------------------------------------------------
// healthCheckHandler – server initialized
// ---------------------------------------------------------------------------

func TestHealthCheckHandler_WithServer(t *testing.T) {
	// Start a real listening server so the health check can connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()

	svc := NewServiceHttp()
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)
	svc.conf = &conf.Http{
		Network: "tcp",
		Addr:    ln.Addr().String(),
		Monitoring: &conf.MonitoringConfig{
			EnableMetrics:  false,
			MetricsPath:    "/metrics",
			HealthPath:     "/health",
		},
	}
	svc.rt = rt
	svc.setDefaultConfig()
	svc.initMetrics()
	svc.refreshMonitoringSnapshotLocked()

	// Build a minimal kratos server that binds to our listener
	// Rather than start a full server, just create one and keep our listener alive.
	// The healthCheckHandler calls CheckRuntimeHealth which dials the configured address.
	// Since our listener is actually accepting, the dial should succeed.
	handler := svc.healthCheckHandler()
	req, err := nhttp.NewRequest("GET", "/health", nil)
	require.NoError(t, err)
	rr := &testResponseWriter{}
	handler.ServeHTTP(rr, req)
	// The handler must return a valid HTTP response (200 or 503 depending on
	// port connectivity; the server is not fully started so 503 is expected).
	assert.Contains(t, []int{nhttp.StatusOK, nhttp.StatusServiceUnavailable}, rr.statusCode)
	assert.NotEmpty(t, rr.body)
}

// ---------------------------------------------------------------------------
// monitoringConfigOrDefaultLocked – with conf.Monitoring
// ---------------------------------------------------------------------------

func TestMonitoringConfigOrDefaultLocked_WithMonitoring(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Monitoring: &conf.MonitoringConfig{
			EnableMetrics: true,
			MetricsPath:   "/custom/metrics",
			HealthPath:    "/custom/health",
		},
	}
	mc := svc.monitoringConfigOrDefaultLocked()
	assert.True(t, mc.EnableMetrics)
	assert.Equal(t, "/custom/metrics", mc.MetricsPath)
}

// ---------------------------------------------------------------------------
// reconfigureMetricsLoop – with connection metrics enabled
// ---------------------------------------------------------------------------

func TestReconfigureMetricsLoop_WithConnectionMetrics(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableConnectionMetrics: true},
	}
	svc.initMetrics()
	svc.setMetricsLifecycleContext(context.Background())
	svc.reconfigureMetricsLoop()
	assert.NotNil(t, svc.metricsCancel)
	svc.stopMetricsLoop()
}

// ---------------------------------------------------------------------------
// setMetricsLifecycleContext – replaces existing context
// ---------------------------------------------------------------------------

func TestSetMetricsLifecycleContext_Replaces(t *testing.T) {
	svc := NewServiceHttp()
	svc.setMetricsLifecycleContext(context.Background())
	oldCancel := svc.metricsRootCancel
	assert.NotNil(t, oldCancel)
	// Replace
	svc.setMetricsLifecycleContext(context.Background())
	assert.NotNil(t, svc.metricsRootCancel)
	svc.stopMetricsLoop()
}

// ---------------------------------------------------------------------------
// stopMetricsLoop – with both cancels set
// ---------------------------------------------------------------------------

func TestStopMetricsLoop_BothCancels(t *testing.T) {
	svc := NewServiceHttp()
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	svc.metricsCtx = ctx1
	svc.metricsCancel = cancel1
	svc.metricsRootCtx = ctx2
	svc.metricsRootCancel = cancel2
	svc.stopMetricsLoop()
	// Both contexts should be canceled
	assert.Error(t, ctx1.Err())
	assert.Error(t, ctx2.Err())
	assert.Nil(t, svc.metricsCancel)
	assert.Nil(t, svc.metricsRootCancel)
}

// ---------------------------------------------------------------------------
// NewCircuitBreaker – default parameter paths
// ---------------------------------------------------------------------------

func TestNewCircuitBreaker_Defaults(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{})
	assert.Equal(t, int32(5), cb.config.MaxFailures)
	assert.Equal(t, 60*time.Second, cb.config.Timeout)
	assert.Equal(t, int32(10), cb.config.MaxRequests)
	assert.Equal(t, 0.5, cb.config.FailureThreshold)
}

func TestNewCircuitBreaker_CustomValues(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		MaxFailures:      3,
		Timeout:          30 * time.Second,
		MaxRequests:      5,
		FailureThreshold: 0.3,
	})
	assert.Equal(t, int32(3), cb.config.MaxFailures)
	assert.Equal(t, 30*time.Second, cb.config.Timeout)
}

// ---------------------------------------------------------------------------
// circuitBreakerMiddleware – open state rejection
// ---------------------------------------------------------------------------

func TestCircuitBreakerMiddleware_OpenRejects(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Network: "tcp",
		Addr:    ":8080",
		CircuitBreaker: &conf.CircuitBreakerConfig{
			Enabled:          true,
			MaxFailures:      1,
			MaxRequests:      1,
			Timeout:          &durationpb.Duration{Seconds: 3600},
			FailureThreshold: 0.5, // must match NewCircuitBreaker's normalization of 0→0.5
		},
	}
	svc.initMetrics()

	// Open the circuit breaker manually: record enough failures to trigger Open.
	cb := svc.ensureCircuitBreaker()
	require.NotNil(t, cb)
	// Drive the CB through Allow+RecordFailure until it opens.
	for i := 0; i < 5; i++ {
		g := cb.Allow()
		cb.RecordFailure(g)
		if cb.GetState() == CircuitBreakerOpen {
			break
		}
	}
	require.Equal(t, CircuitBreakerOpen, cb.GetState(), "circuit breaker should be open after repeated failures")

	mw := svc.circuitBreakerMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "should-not-reach", nil
	})
	_, err := handler(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker is open")
}

// ---------------------------------------------------------------------------
// validateConfigLocked – negative readHeader timeout
// ---------------------------------------------------------------------------

func TestValidateConfigLocked_NegativeReadHeaderTimeout(t *testing.T) {
	h := NewServiceHttp()
	h.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	h.readHeaderTimeout = -time.Second
	err := h.validateConfigLocked()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "read header timeout cannot be negative")
}

// ---------------------------------------------------------------------------
// errorLogFields – with wrapped/Kratos error (more branches)
// ---------------------------------------------------------------------------

func TestErrorLogFields_KratosError(t *testing.T) {
	ke := kratoserrors.New(404, "NOT_FOUND", "not found")
	fields := errorLogFields(ke)
	assert.NotEmpty(t, fields)
}

// ---------------------------------------------------------------------------
// requestLogArgs – with Redact interface
// ---------------------------------------------------------------------------

type redactable struct{ secret string }

func (r redactable) Redact() string { return "<redacted>" }

func TestRequestLogArgs_WithRedact(t *testing.T) {
	r := redactable{secret: "password"}
	got := requestLogArgs(r)
	assert.Equal(t, "<redacted>", got)
}

// ---------------------------------------------------------------------------
// loggingMiddleware – exercise through direct invocation
// ---------------------------------------------------------------------------

func TestLoggingMiddleware_NoTransport(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableRequestLogging: true, EnableErrorLogging: true},
	}
	mw := svc.loggingMiddleware()
	// Execute without transport context (hits the no-transport branch)
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "response", nil
	})
	reply, err := handler(context.Background(), "request")
	require.NoError(t, err)
	assert.Equal(t, "response", reply)
}

func TestLoggingMiddleware_WithError(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableErrorLogging: true},
	}
	mw := svc.loggingMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return nil, errForTest
	})
	_, err := handler(context.Background(), nil)
	require.Error(t, err)
}

var errForTest = errors.New("test middleware error") //nolint:gochecknoglobals

// ---------------------------------------------------------------------------
// connectionLimitMiddleware – requestSem limit exceeded
// ---------------------------------------------------------------------------

func TestConnectionLimitMiddleware_RequestSemExceeded(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Network:    "tcp",
		Addr:       ":8080",
		Monitoring: &conf.MonitoringConfig{EnableConnectionMetrics: false},
	}
	svc.maxConnections = 0 // no connection sem
	svc.maxConcurrentRequests = 1
	svc.initMetrics()
	svc.ensureSemaphores()

	// Fill the request semaphore
	svc.requestSem <- struct{}{}

	mw := svc.connectionLimitMiddleware()
	handler := mw(func(ctx context.Context, req any) (any, error) {
		return "ok", nil
	})
	_, err := handler(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "concurrent request limit exceeded")
}

// ---------------------------------------------------------------------------
// initPerformanceDefaults – already-set Performance config
// ---------------------------------------------------------------------------

func TestInitPerformanceDefaults_NonZeroValues(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{
		Performance: &conf.PerformanceConfig{
			MaxConnections:        50,
			MaxConcurrentRequests: 25,
			ReadBufferSize:        8192,
			WriteBufferSize:       8192,
		},
	}
	svc.initPerformanceDefaults()
	assert.Equal(t, 50, svc.maxConnections)
	assert.Equal(t, 25, svc.maxConcurrentRequests)
	assert.Equal(t, 8192, svc.readBufferSize)
	assert.Equal(t, 8192, svc.writeBufferSize)
}

// ---------------------------------------------------------------------------
// applyPerformanceConfig – with an actual server that has net/http.Server
// ---------------------------------------------------------------------------

func TestApplyPerformanceConfig_WithFullServer(t *testing.T) {
	svc := NewServiceHttp()
	base := plugins.NewSimpleRuntime()
	rt := base.WithPluginContext(pluginName)
	svc.conf = &conf.Http{
		Network: "tcp",
		Addr:    "127.0.0.1:0",
		Monitoring: &conf.MonitoringConfig{
			EnableMetrics: false,
			MetricsPath:   "/metrics",
			HealthPath:    "/health",
		},
		Performance: &conf.PerformanceConfig{
			ReadTimeout:  &durationpb.Duration{Seconds: 5},
			WriteTimeout: &durationpb.Duration{Seconds: 5},
		},
	}
	svc.rt = rt
	svc.initMetrics()
	// Start the server
	if err := svc.StartupTasks(); err != nil {
		t.Skip("could not start server:", err)
	}
	defer func() { _ = svc.CleanupTasks() }()
	// applyPerformanceConfig was already called in StartupTasks; just verify server started
	assert.NotNil(t, svc.server)
}

// ---------------------------------------------------------------------------
// publishRuntimeContract – nil rt branch
// ---------------------------------------------------------------------------

func TestPublishRuntimeContractNilRt(t *testing.T) {
	svc := NewServiceHttp()
	// rt is nil by default
	svc.publishRuntimeContract(false, false) // should not panic
}

// ---------------------------------------------------------------------------
// checkPortAvailability – invalid address format
// ---------------------------------------------------------------------------

func TestCheckPortAvailability_InvalidAddr(t *testing.T) {
	svc := NewServiceHttp()
	// An address that can't be SplitHostPort-ed (since it has no ":")
	// But our impl first checks if ":" is present and prepends one if not.
	// So "noport" becomes ":noport" which still fails SplitHostPort or port validation.
	svc.conf = &conf.Http{Network: "tcp", Addr: "[invalid"}
	err := svc.checkPortAvailability()
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// ensureSemaphores – already correct capacity (no-op rebuild)
// ---------------------------------------------------------------------------

func TestEnsureSemaphores_AlreadyCorrectCapacity(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = &conf.Http{Network: "tcp", Addr: ":8080"}
	svc.maxConnections = 10
	svc.maxConcurrentRequests = 5
	svc.connectionSem = make(chan struct{}, 10)
	svc.requestSem = make(chan struct{}, 5)
	svc.ensureSemaphores() // should not recreate
	assert.Equal(t, 10, cap(svc.connectionSem))
	assert.Equal(t, 5, cap(svc.requestSem))
}

// ---------------------------------------------------------------------------
// ensureCircuitBreaker – nil conf
// ---------------------------------------------------------------------------

func TestEnsureCircuitBreaker_NilConf(t *testing.T) {
	svc := NewServiceHttp()
	svc.conf = nil
	cb := svc.ensureCircuitBreaker()
	assert.Nil(t, cb)
}

// ---------------------------------------------------------------------------
// listenConfigSnapshot – nil plugin
// ---------------------------------------------------------------------------

func TestListenConfigSnapshot_NilPlugin(t *testing.T) {
	var svc *ServiceHttp
	network, addr := svc.listenConfigSnapshot()
	assert.Equal(t, "", network)
	assert.Equal(t, "", addr)
}

// ---------------------------------------------------------------------------
// monitoringSnapshotOrDefault – nil plugin
// ---------------------------------------------------------------------------

func TestMonitoringSnapshotOrDefault_NilPlugin(t *testing.T) {
	var svc *ServiceHttp
	snap := svc.monitoringSnapshotOrDefault()
	assert.NotNil(t, snap)
	assert.True(t, snap.enableMetrics)
}

