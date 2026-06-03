package http

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-lynx/lynx"
	"github.com/go-lynx/lynx-http/conf"
	"google.golang.org/protobuf/proto"
)

const (
	defaultMetricsPath = "/metrics"
	defaultHealthPath  = "/health"
)

var sensitiveHeaderKeys = map[string]struct{}{
	"authorization": {},
	"cookie":        {},
	"set-cookie":    {},
	"x-api-key":     {},
	"x-auth-token":  {},
}

type monitoringSnapshot struct {
	enableMetrics           bool
	enableRequestLogging    bool
	enableErrorLogging      bool
	enableRouteMetrics      bool
	enableConnectionMetrics bool
	enableQueueMetrics      bool
	enableErrorTypeMetrics  bool
	metricsPath             string
	healthPath              string
}

func currentLynxApp() *lynx.LynxApp {
	return lynx.Lynx()
}

// Use reflection so the plugin stays compatible across lynx versions that may
// expose application name via different APIs.
func currentLynxName() string {
	if app := currentLynxApp(); app != nil {
		value := reflect.ValueOf(app)
		if method := value.MethodByName("Name"); method.IsValid() && method.Type().NumIn() == 0 &&
			method.Type().NumOut() == 1 && method.Type().Out(0).Kind() == reflect.String {
			return method.Call(nil)[0].String()
		}
	}
	return ""
}

func (h *ServiceHttp) monitoringConfigOrDefault() *conf.MonitoringConfig {
	snap := h.monitoringSnapshotOrDefault()
	return &conf.MonitoringConfig{
		EnableMetrics:           snap.enableMetrics,
		EnableRequestLogging:    snap.enableRequestLogging,
		EnableErrorLogging:      snap.enableErrorLogging,
		EnableRouteMetrics:      snap.enableRouteMetrics,
		EnableConnectionMetrics: snap.enableConnectionMetrics,
		EnableQueueMetrics:      snap.enableQueueMetrics,
		EnableErrorTypeMetrics:  snap.enableErrorTypeMetrics,
		MetricsPath:             snap.metricsPath,
		HealthPath:              snap.healthPath,
	}
}

func (h *ServiceHttp) monitoringConfigOrDefaultLocked() *conf.MonitoringConfig {
	if h == nil || h.conf == nil || h.conf.Monitoring == nil {
		return defaultMonitoringConfig()
	}
	cfg, ok := proto.Clone(h.conf.Monitoring).(*conf.MonitoringConfig)
	if !ok || cfg == nil {
		cfg = &conf.MonitoringConfig{}
	}
	if strings.TrimSpace(cfg.MetricsPath) == "" {
		cfg.MetricsPath = defaultMetricsPath
	}
	if strings.TrimSpace(cfg.HealthPath) == "" {
		cfg.HealthPath = defaultHealthPath
	}
	return cfg
}

func defaultMonitoringConfig() *conf.MonitoringConfig {
	return &conf.MonitoringConfig{
		EnableMetrics:           true,
		EnableRequestLogging:    true,
		EnableErrorLogging:      true,
		EnableRouteMetrics:      true,
		EnableConnectionMetrics: true,
		EnableQueueMetrics:      true,
		EnableErrorTypeMetrics:  true,
		MetricsPath:             defaultMetricsPath,
		HealthPath:              defaultHealthPath,
	}
}

func defaultMonitoringSnapshot() *monitoringSnapshot {
	return &monitoringSnapshot{
		enableMetrics:           true,
		enableRequestLogging:    true,
		enableErrorLogging:      true,
		enableRouteMetrics:      true,
		enableConnectionMetrics: true,
		enableQueueMetrics:      true,
		enableErrorTypeMetrics:  true,
		metricsPath:             defaultMetricsPath,
		healthPath:              defaultHealthPath,
	}
}

func monitoringSnapshotFromConfig(cfg *conf.MonitoringConfig) *monitoringSnapshot {
	if cfg == nil {
		return defaultMonitoringSnapshot()
	}
	snap := &monitoringSnapshot{
		enableMetrics:           cfg.EnableMetrics,
		enableRequestLogging:    cfg.EnableRequestLogging,
		enableErrorLogging:      cfg.EnableErrorLogging,
		enableRouteMetrics:      cfg.EnableRouteMetrics,
		enableConnectionMetrics: cfg.EnableConnectionMetrics,
		enableQueueMetrics:      cfg.EnableQueueMetrics,
		enableErrorTypeMetrics:  cfg.EnableErrorTypeMetrics,
		metricsPath:             strings.TrimSpace(cfg.MetricsPath),
		healthPath:              strings.TrimSpace(cfg.HealthPath),
	}
	if snap.metricsPath == "" {
		snap.metricsPath = defaultMetricsPath
	}
	if snap.healthPath == "" {
		snap.healthPath = defaultHealthPath
	}
	return snap
}

func (h *ServiceHttp) refreshMonitoringSnapshotLocked() {
	if h == nil {
		return
	}
	if h.conf == nil {
		h.monitoringSnapshot.Store(defaultMonitoringSnapshot())
		return
	}
	h.monitoringSnapshot.Store(monitoringSnapshotFromConfig(h.conf.Monitoring))
}

func (h *ServiceHttp) monitoringSnapshotOrDefault() *monitoringSnapshot {
	if h == nil {
		return defaultMonitoringSnapshot()
	}
	if snap, ok := h.monitoringSnapshot.Load().(*monitoringSnapshot); ok && snap != nil {
		return snap
	}
	h.confMu.RLock()
	var snap *monitoringSnapshot
	if h.conf != nil {
		snap = monitoringSnapshotFromConfig(h.conf.Monitoring)
	}
	h.confMu.RUnlock()
	if snap == nil {
		snap = defaultMonitoringSnapshot()
	}
	h.monitoringSnapshot.Store(snap)
	return snap
}

func (h *ServiceHttp) listenConfigSnapshot() (network, addr string) {
	if h == nil {
		return "", ""
	}
	h.confMu.RLock()
	defer h.confMu.RUnlock()
	if h.conf == nil {
		return "", ""
	}
	return h.conf.Network, h.conf.Addr
}

func (h *ServiceHttp) requestLoggingEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableRequestLogging
}

func (h *ServiceHttp) errorLoggingEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableErrorLogging
}

func requestLoggingEnabled(service *ServiceHttp) bool {
	return service.requestLoggingEnabled()
}

func errorLoggingEnabled(service *ServiceHttp) bool {
	return service.errorLoggingEnabled()
}

func (h *ServiceHttp) metricsEndpointEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableMetrics
}

func (h *ServiceHttp) metricsPath() string {
	return h.monitoringSnapshotOrDefault().metricsPath
}

func (h *ServiceHttp) healthPath() string {
	return h.monitoringSnapshotOrDefault().healthPath
}

func (h *ServiceHttp) routeMetricsEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableRouteMetrics
}

func (h *ServiceHttp) connectionMetricsEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableConnectionMetrics
}

func (h *ServiceHttp) queueMetricsEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableQueueMetrics
}

func (h *ServiceHttp) errorTypeMetricsEnabled() bool {
	return h.monitoringSnapshotOrDefault().enableErrorTypeMetrics
}

func (h *ServiceHttp) recordErrorMetric(method, path, errorType string) {
	if h == nil || h.errorCounter == nil {
		return
	}
	if !h.errorTypeMetricsEnabled() {
		errorType = "error"
	}
	h.errorCounter.WithLabelValues(method, path, errorType).Inc()
}

func requestMetadata(ctx context.Context) (method, path string) {
	method = "unknown"
	path = "unknown"
	if tr, ok := transport.FromServerContext(ctx); ok {
		if headerMethod := strings.TrimSpace(tr.RequestHeader().Get("X-HTTP-Method")); headerMethod != "" {
			method = headerMethod
		}
		if op := strings.TrimSpace(tr.Operation()); op != "" {
			path = op
		} else if endpoint := strings.TrimSpace(tr.Endpoint()); endpoint != "" {
			path = endpoint
		}
	}
	return method, path
}

func sanitizeHeaders(header transport.Header) map[string]string {
	if header == nil {
		return nil
	}
	headers := make(map[string]string, len(header.Keys()))
	for _, key := range header.Keys() {
		value := header.Get(key)
		if _, sensitive := sensitiveHeaderKeys[strings.ToLower(key)]; sensitive {
			headers[key] = "<redacted>"
			continue
		}
		headers[key] = value
	}
	return headers
}

func summarizePayload(payload any) string {
	if payload == nil {
		return "<nil>"
	}
	if msg, ok := payload.(proto.Message); ok {
		return fmt.Sprintf("<%T>", msg)
	}
	return fmt.Sprintf("<%T>", payload)
}

func (h *ServiceHttp) ensureSemaphores() {
	h.confMu.Lock()
	defer h.confMu.Unlock()

	if cap(h.connectionSem) != h.maxConnections {
		if h.maxConnections > 0 {
			h.connectionSem = make(chan struct{}, h.maxConnections)
		} else {
			h.connectionSem = nil
		}
	}
	if cap(h.requestSem) != h.maxConcurrentRequests {
		if h.maxConcurrentRequests > 0 {
			h.requestSem = make(chan struct{}, h.maxConcurrentRequests)
		} else {
			h.requestSem = nil
		}
	}
}

func (h *ServiceHttp) ensureCircuitBreaker() *CircuitBreaker {
	if h == nil {
		return nil
	}
	h.confMu.Lock()
	defer h.confMu.Unlock()

	if h == nil || h.conf == nil || h.conf.CircuitBreaker == nil || !h.conf.CircuitBreaker.Enabled {
		h.circuitBreaker = nil
		return nil
	}

	timeout := 60 * time.Second
	if h.conf.CircuitBreaker.Timeout != nil {
		timeout = h.conf.CircuitBreaker.Timeout.AsDuration()
	}
	cfg := CircuitBreakerConfig{
		MaxFailures:      h.conf.CircuitBreaker.MaxFailures,
		Timeout:          timeout,
		MaxRequests:      h.conf.CircuitBreaker.MaxRequests,
		FailureThreshold: h.conf.CircuitBreaker.FailureThreshold,
	}

	if h.circuitBreaker == nil || h.circuitBreaker.config != cfg {
		h.circuitBreaker = NewCircuitBreaker(cfg)
	}
	return h.circuitBreaker
}

func (h *ServiceHttp) reconfigureMetricsLoop() {
	if h == nil {
		return
	}

	if h.metricsCancel != nil {
		h.metricsCancel()
		h.metricsCancel = nil
		h.metricsCtx = nil
	}

	if h.connectionPoolUsage != nil && h.connectionMetricsEnabled() {
		parentCtx := h.metricsRootCtx
		if parentCtx == nil {
			parentCtx = context.Background()
		}
		h.metricsCtx, h.metricsCancel = context.WithCancel(parentCtx)
		go h.updateConnectionPoolMetrics(h.metricsCtx)
	}
}

func (h *ServiceHttp) setMetricsLifecycleContext(ctx context.Context) {
	if h == nil {
		return
	}

	if h.metricsRootCancel != nil {
		h.metricsRootCancel()
		h.metricsRootCancel = nil
		h.metricsRootCtx = nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	h.metricsRootCtx, h.metricsRootCancel = context.WithCancel(context.WithoutCancel(ctx))
}

func (h *ServiceHttp) stopMetricsLoop() {
	if h == nil {
		return
	}

	if h.metricsCancel != nil {
		h.metricsCancel()
		h.metricsCancel = nil
		h.metricsCtx = nil
	}
	if h.metricsRootCancel != nil {
		h.metricsRootCancel()
		h.metricsRootCancel = nil
		h.metricsRootCtx = nil
	}
}

func resetOnce(once *sync.Once) {
	if once != nil {
		*once = sync.Once{}
	}
}
