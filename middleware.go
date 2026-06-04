// Package http implements the HTTP server plugin for the Lynx framework.
package http

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/go-kratos/kratos/contrib/middleware/validate/v2"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	"github.com/go-kratos/kratos/v2/middleware/tracing"
	"github.com/go-lynx/lynx-http/conf"
	"github.com/go-lynx/lynx/log"
	"google.golang.org/protobuf/proto"
)

// buildMiddlewares builds the middleware chain based on configuration.
func (h *ServiceHttp) buildMiddlewares() []middleware.Middleware {
	h.confMu.RLock()
	defer h.confMu.RUnlock()

	var middlewares []middleware.Middleware

	cfg := h.conf
	// Guard: conf must be loaded (e.g. after InitializeResources or valid Configure)
	if cfg == nil {
		log.Warnf("buildMiddlewares called with nil conf, returning empty middleware chain")
		return middlewares
	}

	middlewareCfg := cfg.Middleware
	if middlewareCfg == nil {
		middlewareCfg = &conf.MiddlewareConfig{
			EnableTracing:    true,
			EnableLogging:    true,
			EnableMetrics:    true,
			EnableRecovery:   true,
			EnableRateLimit:  true,
			EnableValidation: true,
		}
	}

	// Order matters: middlewares execute outermost-first in the order appended.
	// Tracing runs first so the span/trace ID is in context for logging, metrics, and the
	// downstream handler; recovery sits after validation so panics in any layer are caught.
	if middlewareCfg.EnableTracing {
		middlewares = append(middlewares, tracing.Server(tracing.WithTracerName(currentLynxName())))
		log.Infof("Tracing middleware enabled")
	}

	if middlewareCfg.EnableLogging {
		middlewares = append(middlewares, h.loggingMiddleware())
		log.Infof("Logging middleware enabled")
	}

	// Metrics: use either standalone metricsMiddleware or TracerLogPackWithMetrics to avoid duplicate metrics
	if middlewareCfg.EnableTracing && middlewareCfg.EnableLogging && middlewareCfg.EnableMetrics {
		middlewares = append(middlewares, TracerLogPackWithMetrics(h))
		log.Infof("TracerLogPackWithMetrics middleware enabled (tracing + logging + metrics)")
	} else if middlewareCfg.EnableMetrics {
		middlewares = append(middlewares, h.metricsMiddleware())
		log.Infof("Metrics middleware enabled")
	}

	if middlewareCfg.EnableValidation {
		middlewares = append(middlewares, validate.ProtoValidate())
		log.Infof("Validation middleware enabled")
	}

	if middlewareCfg.EnableRecovery {
		middlewares = append(middlewares, h.recoveryMiddleware())
		log.Infof("Recovery middleware enabled")
	}

	if middlewareCfg.EnableRateLimit {
		middlewares = append(middlewares, h.rateLimitMiddleware())
		log.Infof("Rate limit middleware enabled")
	}

	// Concurrent request limit middleware (limits in-flight requests, not TCP connections)
	if h.maxConnections > 0 || h.maxConcurrentRequests > 0 {
		middlewares = append(middlewares, h.connectionLimitMiddleware())
		log.Infof("Concurrent request limit middleware enabled")
	}

	// Circuit breaker middleware
	middlewares = append(middlewares, h.circuitBreakerMiddleware())
	log.Infof("Circuit breaker middleware enabled")

	// Configure rate limit middleware using Lynx control plane HTTP rate limit policy
	// If a rate limit middleware exists, append it
	var rl middleware.Middleware
	if app := currentLynxApp(); app != nil {
		if cp := app.GetControlPlane(); cp != nil {
			rl = cp.HTTPRateLimit()
		}
	}
	if rl != nil && middlewareCfg.EnableRateLimit {
		middlewares = append(middlewares, rl)
		log.Infof("Control plane rate limit middleware enabled")
	}

	// Custom middleware and ordering features will be implemented in future versions
	// These features require additional protobuf definitions and implementation logic
	// Tracked in enhancement request #HTTP-1234

	return middlewares
}

// connectionLimitMiddleware returns a middleware that limits concurrent in-flight requests (and optionally a separate cap for connection-pool metrics).
// Semaphores are initialized once and reused across all requests.
func (h *ServiceHttp) connectionLimitMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			h.ensureSemaphores()

			// First semaphore caps total in-flight requests (maxConnections).
			if h.connectionSem != nil {
				select {
				case h.connectionSem <- struct{}{}:
					if h.maxConnections > 0 && h.connectionMetricsEnabled() {
						newCount := atomic.AddInt32(&h.activeConnectionsCount, 1)
						h.UpdateConnectionPoolUsage(newCount, int32(h.maxConnections))
					}
					defer func() {
						<-h.connectionSem
						if h.maxConnections > 0 && h.connectionMetricsEnabled() {
							newCount := atomic.AddInt32(&h.activeConnectionsCount, -1)
							if newCount < 0 {
								atomic.StoreInt32(&h.activeConnectionsCount, 0)
								newCount = 0
							}
							h.UpdateConnectionPoolUsage(newCount, int32(h.maxConnections))
						}
					}()
				default:
					method, path := requestMetadata(ctx)
					h.recordErrorMetric(method, path, "connection_limit_exceeded")
					return nil, fmt.Errorf("concurrent request limit exceeded: max %d", h.maxConnections)
				}
			}

			// Second semaphore caps concurrent requests (maxConcurrentRequests).
			if h.requestSem != nil {
				select {
				case h.requestSem <- struct{}{}:
					defer func() { <-h.requestSem }()
				default:
					method, path := requestMetadata(ctx)
					h.recordErrorMetric(method, path, "request_limit_exceeded")
					return nil, fmt.Errorf("concurrent request limit exceeded: max %d requests", h.maxConcurrentRequests)
				}
			}

			return handler(ctx, req)
		}
	}
}

// recoveryMiddleware returns a recovery middleware.
func (h *ServiceHttp) recoveryMiddleware() middleware.Middleware {
	return recovery.Recovery(
		recovery.WithHandler(func(ctx context.Context, req, err any) error {
			log.ErrorCtx(ctx, "Panic recovered", "error", err)

			// Record error metrics
			method, path := requestMetadata(ctx)
			h.recordErrorMetric(method, path, "panic")

			return nil
		}),
	)
}

// rateLimitMiddleware returns a rate limit middleware.
func (h *ServiceHttp) rateLimitMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			method, path := requestMetadata(ctx)

			if h.requestQueueLength != nil && h.queueMetricsEnabled() {
				h.requestQueueLength.WithLabelValues(path).Inc()
				defer h.requestQueueLength.WithLabelValues(path).Dec()
			}

			if h.rateLimiter != nil && !h.rateLimiter.Allow() {
				h.recordErrorMetric(method, path, "rate_limit_exceeded")
				return nil, fmt.Errorf("rate limit exceeded")
			}
			return handler(ctx, req)
		}
	}
}

// metricsMiddleware returns a metrics middleware.
func (h *ServiceHttp) metricsMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req any) (reply any, err error) {
			start := time.Now()

			method := "unknown"
			path := "unknown"
			route := "unknown"

			method, path = requestMetadata(ctx)
			route = path

			if h.activeConnections != nil && h.connectionMetricsEnabled() {
				_, addr := h.listenConfigSnapshot()
				if addr == "" {
					addr = "unknown"
				}
				h.activeConnections.WithLabelValues(addr).Inc()
				defer h.activeConnections.WithLabelValues(addr).Dec()
			}

			if h.inflightRequests != nil {
				h.inflightRequests.WithLabelValues(path).Inc()
				defer h.inflightRequests.WithLabelValues(path).Dec()
			}

			reply, err = handler(ctx, req)

			duration := time.Since(start).Seconds()
			if h.requestDuration != nil {
				h.requestDuration.WithLabelValues(method, path).Observe(duration)
			}

			if h.requestCounter != nil {
				status := "success"
				if err != nil {
					status = "error"
				}
				h.requestCounter.WithLabelValues(method, path, status).Inc()
			}

			// Response size is only measurable for proto replies.
			if h.responseSize != nil && reply != nil {
				if msg, ok := reply.(proto.Message); ok {
					if data, marshalErr := proto.Marshal(msg); marshalErr == nil {
						h.responseSize.WithLabelValues(method, path).Observe(float64(len(data)))
					}
				}
			}

			if h.routeRequestDuration != nil && h.routeMetricsEnabled() {
				h.routeRequestDuration.WithLabelValues(route, method).Observe(duration)
			}

			if h.routeRequestCounter != nil && h.routeMetricsEnabled() {
				status := "success"
				if err != nil {
					status = "error"
				}
				h.routeRequestCounter.WithLabelValues(route, method, status).Inc()
			}

			return reply, err
		}
	}
}
