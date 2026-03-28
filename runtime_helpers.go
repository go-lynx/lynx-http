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
	if h == nil || h.conf == nil || h.conf.Monitoring == nil {
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
	cfg := *h.conf.Monitoring
	if strings.TrimSpace(cfg.MetricsPath) == "" {
		cfg.MetricsPath = defaultMetricsPath
	}
	if strings.TrimSpace(cfg.HealthPath) == "" {
		cfg.HealthPath = defaultHealthPath
	}
	return &cfg
}

func (h *ServiceHttp) requestLoggingEnabled() bool {
	return h.monitoringConfigOrDefault().EnableRequestLogging
}

func (h *ServiceHttp) errorLoggingEnabled() bool {
	return h.monitoringConfigOrDefault().EnableErrorLogging
}

func requestLoggingEnabled(service *ServiceHttp) bool {
	return service.requestLoggingEnabled()
}

func errorLoggingEnabled(service *ServiceHttp) bool {
	return service.errorLoggingEnabled()
}

func (h *ServiceHttp) metricsEndpointEnabled() bool {
	return h.monitoringConfigOrDefault().EnableMetrics
}

func (h *ServiceHttp) metricsPath() string {
	return h.monitoringConfigOrDefault().MetricsPath
}

func (h *ServiceHttp) healthPath() string {
	return h.monitoringConfigOrDefault().HealthPath
}

func (h *ServiceHttp) routeMetricsEnabled() bool {
	return h.monitoringConfigOrDefault().EnableRouteMetrics
}

func (h *ServiceHttp) connectionMetricsEnabled() bool {
	return h.monitoringConfigOrDefault().EnableConnectionMetrics
}

func (h *ServiceHttp) queueMetricsEnabled() bool {
	return h.monitoringConfigOrDefault().EnableQueueMetrics
}

func (h *ServiceHttp) errorTypeMetricsEnabled() bool {
	return h.monitoringConfigOrDefault().EnableErrorTypeMetrics
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

func summarizePayload(payload interface{}) string {
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
	if h == nil || h.conf == nil || h.conf.CircuitBreaker == nil || !h.conf.CircuitBreaker.Enabled {
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

	h.confMu.Lock()
	defer h.confMu.Unlock()

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
		h.metricsCtx, h.metricsCancel = context.WithCancel(context.Background())
		go h.updateConnectionPoolMetrics(h.metricsCtx)
	}
}

func resetOnce(once *sync.Once) {
	if once != nil {
		*once = sync.Once{}
	}
}
