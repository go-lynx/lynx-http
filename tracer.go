package http

import (
	"context"
	"fmt"
	"time"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-lynx/lynx/log"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

const (
	contentTypeKey  = "Content-Type"
	jsonContentType = "application/json"
	traceIDNone     = "none"
	spanIDNone      = "none"

	httpRequestLogFormat  = "[HTTP Request] api=%s endpoint=%s client-ip=%s headers=%s body=%s"
	httpResponseLogFormat = "[HTTP Response] api=%s endpoint=%s duration=%v error=%v headers=%s body=%s"
)

// tracePropagator extracts/injects W3C Trace Context (traceparent, tracestate) and Baggage from request headers.
var tracePropagator = propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})

// transportHeaderCarrier adapts transport.Header to propagation.TextMapCarrier so we can extract trace from request headers.
type transportHeaderCarrier struct{ transport.Header }

func (c transportHeaderCarrier) Get(key string) string {
	return c.Header.Get(key)
}

func (c transportHeaderCarrier) Set(key, value string) {
	c.Header.Set(key, value)
}

func (c transportHeaderCarrier) Keys() []string {
	return c.Header.Keys()
}

// extractTraceContextFromRequest tries to extract W3C traceparent (and optional tracestate/baggage) from request headers
// and inject into ctx. If the upstream middleware already set a valid span, this is a no-op; otherwise gateways/proxies
// that send traceparent will get a continuous trace.
func extractTraceContextFromRequest(ctx context.Context, reqHeader transport.Header) context.Context {
	if reqHeader == nil {
		return ctx
	}
	return tracePropagator.Extract(ctx, transportHeaderCarrier{reqHeader})
}

// traceIDAndSpanIDFromSpan returns Trace-Id and Span-Id for response headers. If span is invalid (e.g. no tracer),
// returns "none" to avoid returning all-zero IDs.
func traceIDAndSpanIDFromSpan(span trace.SpanContext) (traceID, spanID string) {
	if span.IsValid() {
		return span.TraceID().String(), span.SpanID().String()
	}
	return traceIDNone, spanIDNone
}

// getClientIP returns the client IP address.
func getClientIP(header transport.Header) string {
	for _, key := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if ip := header.Get(key); ip != "" {
			return ip
		}
	}
	return "unknown"
}

// TracerLogPack returns middleware that adds trace IDs and Content-Type headers to the response.
// It extracts trace from context (or from request headers like W3C traceparent if not yet in context) and sets "Trace-Id" and "Span-Id" in response headers. Invalid/empty span is returned as "none".
func TracerLogPack() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			start := time.Now()
			var tr transport.Transporter
			var ok bool
			if tr, ok = transport.FromServerContext(ctx); !ok {
				log.WarnfCtx(ctx, "Failed to get transport from context, proceeding without tracing")
				return handler(ctx, req)
			}

			// Explicitly extract W3C traceparent (and baggage) from request headers into context, so gateways/proxies that send traceparent get a continuous trace even if upstream middleware did not run yet.
			ctx = extractTraceContextFromRequest(ctx, tr.RequestHeader())
			span := trace.SpanContextFromContext(ctx)
			traceID, spanID := traceIDAndSpanIDFromSpan(span)

			endpoint := tr.Endpoint()
			clientIP := getClientIP(tr.RequestHeader())
			api := tr.Operation()

			defer func() {
				header := tr.ReplyHeader()
				header.Set("Trace-Id", traceID)
				header.Set("Span-Id", spanID)
				if _, ok := reply.(proto.Message); ok {
					header.Set(contentTypeKey, jsonContentType)
				}
			}()

			// Log the request
			if requestLoggingEnabled(nil) {
				headersStr := fmt.Sprintf("%#v", sanitizeHeaders(tr.RequestHeader()))
				log.InfofCtx(ctx, httpRequestLogFormat, api, endpoint, clientIP, headersStr, summarizePayload(req))
			}

			reply, err = handler(ctx, req)

			duration := time.Since(start)
			respHeadersStr := fmt.Sprintf("%#v", sanitizeHeaders(tr.ReplyHeader()))
			respBody := summarizePayload(reply)
			if err != nil && errorLoggingEnabled(nil) {
				log.ErrorfCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			} else if requestLoggingEnabled(nil) {
				log.InfofCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			}

			return reply, err
		}
	}
}

// TracerLogPackWithMetrics returns an enhanced middleware that integrates tracing, logging, and monitoring metrics.
// Trace is extracted from request headers (W3C traceparent) when not already in context; invalid span is returned as "none" in response headers.
func TracerLogPackWithMetrics(service *ServiceHttp) middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}

			start := time.Now()
			var tr transport.Transporter
			var ok bool
			if tr, ok = transport.FromServerContext(ctx); !ok {
				log.WarnfCtx(ctx, "Failed to get transport from context, proceeding without tracing")
				return handler(ctx, req)
			}

			ctx = extractTraceContextFromRequest(ctx, tr.RequestHeader())
			span := trace.SpanContextFromContext(ctx)
			traceID, spanID := traceIDAndSpanIDFromSpan(span)

			endpoint := tr.Endpoint()
			clientIP := getClientIP(tr.RequestHeader())
			api := tr.Operation()
			method, metricPath := requestMetadata(ctx)

			defer func() {
				header := tr.ReplyHeader()
				header.Set("Trace-Id", traceID)
				header.Set("Span-Id", spanID)
				if _, ok := reply.(proto.Message); ok {
					header.Set(contentTypeKey, jsonContentType)
				}
			}()

			if service.requestLoggingEnabled() {
				headersStr := fmt.Sprintf("%#v", sanitizeHeaders(tr.RequestHeader()))
				log.InfofCtx(ctx, httpRequestLogFormat, api, endpoint, clientIP, headersStr, summarizePayload(req))
			}

			// Inflight counter and request size metrics
			if service != nil && service.inflightRequests != nil {
				service.inflightRequests.WithLabelValues(api).Inc()
				defer service.inflightRequests.WithLabelValues(api).Dec()
			}

			if service != nil && service.requestSize != nil {
				if msg, ok := req.(proto.Message); ok {
					if data, e := proto.Marshal(msg); e == nil {
						service.requestSize.WithLabelValues(method, metricPath).Observe(float64(len(data)))
					}
				}
			}

			// Handle the request
			reply, err = handler(ctx, req)

			// Choose log level based on presence of error
			duration := time.Since(start)
			respHeadersStr := fmt.Sprintf("%#v", sanitizeHeaders(tr.ReplyHeader()))
			respBody := summarizePayload(reply)
			if err != nil && service.errorLoggingEnabled() {
				log.ErrorfCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			} else if service.requestLoggingEnabled() {
				log.InfofCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			}

			// Record monitoring metrics (if the service instance is available)
			if service != nil {
				// Record request duration
				if service.requestDuration != nil {
					service.requestDuration.WithLabelValues(method, metricPath).Observe(duration.Seconds())
				}

				// Record request count
				if service.requestCounter != nil {
					status := "success"
					if err != nil {
						status = "error"
					}
					service.requestCounter.WithLabelValues(method, metricPath, status).Inc()
				}

				// Record response size
				if service.responseSize != nil && reply != nil {
					if msg, ok := reply.(proto.Message); ok {
						if data, marshalErr := proto.Marshal(msg); marshalErr == nil {
							service.responseSize.WithLabelValues(method, metricPath).Observe(float64(len(data)))
						}
					}
				}

				// Record errors
				if err != nil {
					service.recordErrorMetric(method, metricPath, "tracer_error")
				}
			}

			return reply, err
		}
	}
}
