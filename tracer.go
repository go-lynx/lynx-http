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
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	maxBodySize     = 1024 * 1024 // 1MB
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

// safeProtoToJSON safely marshals a proto message to JSON.
func safeProtoToJSON(msg proto.Message) (string, error) {
	body, err := protojson.Marshal(msg)
	if err != nil {
		return "", err
	}
	if len(body) > maxBodySize {
		return fmt.Sprintf("<body too large, size: %d bytes>", len(body)), nil
	}
	return string(body), nil
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
			var reqBody string
			if msg, ok := req.(proto.Message); ok {
				if body, jsonErr := safeProtoToJSON(msg); jsonErr == nil {
					reqBody = body
				} else {
					reqBody = fmt.Sprintf("<failed to marshal request: %v>", jsonErr)
				}
			} else {
				reqBody = fmt.Sprintf("%#v", req)
			}

			// Collect all request headers
			headers := make(map[string]string)
			for _, key := range tr.RequestHeader().Keys() {
				headers[key] = tr.RequestHeader().Get(key)
			}
			headersStr := fmt.Sprintf("%#v", headers)

			// Log with Info level for production monitoring
			log.InfofCtx(ctx, httpRequestLogFormat, api, endpoint, clientIP, headersStr, reqBody)

			reply, err = handler(ctx, req)

			var respBody string
			if msg, ok := reply.(proto.Message); ok {
				if body, jsonErr := safeProtoToJSON(msg); jsonErr == nil {
					respBody = body
				} else {
					respBody = fmt.Sprintf("<failed to marshal response: %v>", jsonErr)
				}
			} else {
				respBody = fmt.Sprintf("%#v", reply)
			}

			respHeaders := make(map[string]string)
			for _, key := range tr.ReplyHeader().Keys() {
				respHeaders[key] = tr.ReplyHeader().Get(key)
			}
			respHeadersStr := fmt.Sprintf("%#v", respHeaders)

			duration := time.Since(start)
			if err != nil {
				log.ErrorfCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			} else {
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

			defer func() {
				header := tr.ReplyHeader()
				header.Set("Trace-Id", traceID)
				header.Set("Span-Id", spanID)
				if _, ok := reply.(proto.Message); ok {
					header.Set(contentTypeKey, jsonContentType)
				}
			}()

			var reqBody string
			if msg, ok := req.(proto.Message); ok {
				if body, jsonErr := safeProtoToJSON(msg); jsonErr == nil {
					reqBody = body
				} else {
					reqBody = fmt.Sprintf("<failed to marshal request: %v>", jsonErr)
				}
			} else {
				reqBody = fmt.Sprintf("%#v", req)
			}

			headers := make(map[string]string)
			for _, key := range tr.RequestHeader().Keys() {
				headers[key] = tr.RequestHeader().Get(key)
			}
			headersStr := fmt.Sprintf("%#v", headers)

			log.InfofCtx(ctx, httpRequestLogFormat, api, endpoint, clientIP, headersStr, reqBody)

			// Inflight counter and request size metrics
			if service != nil && service.inflightRequests != nil {
				service.inflightRequests.WithLabelValues(api).Inc()
				defer service.inflightRequests.WithLabelValues(api).Dec()
			}

			if service != nil && service.requestSize != nil {
				if msg, ok := req.(proto.Message); ok {
					if data, e := proto.Marshal(msg); e == nil {
						service.requestSize.WithLabelValues("POST", api).Observe(float64(len(data)))
					}
				}
			}

			// Handle the request
			reply, err = handler(ctx, req)

			// Log the response
			var respBody string
			if msg, ok := reply.(proto.Message); ok {
				if body, jsonErr := safeProtoToJSON(msg); jsonErr == nil {
					respBody = body
				} else {
					respBody = fmt.Sprintf("<failed to marshal response: %v>", jsonErr)
				}
			} else {
				respBody = fmt.Sprintf("%#v", reply)
			}

			// Collect all response headers
			respHeaders := make(map[string]string)
			for _, key := range tr.ReplyHeader().Keys() {
				respHeaders[key] = tr.ReplyHeader().Get(key)
			}
			respHeadersStr := fmt.Sprintf("%#v", respHeaders)

			// Choose log level based on presence of error
			duration := time.Since(start)
			if err != nil {
				log.ErrorfCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			} else {
				log.InfofCtx(ctx, httpResponseLogFormat,
					api, endpoint, duration, err, respHeadersStr, respBody)
			}

			// Record monitoring metrics (if the service instance is available)
			if service != nil {
				// Record request duration
				if service.requestDuration != nil {
					service.requestDuration.WithLabelValues("POST", api).Observe(duration.Seconds())
				}

				// Record request count
				if service.requestCounter != nil {
					status := "success"
					if err != nil {
						status = "error"
					}
					service.requestCounter.WithLabelValues("POST", api, status).Inc()
				}

				// Record response size
				if service.responseSize != nil && reply != nil {
					if msg, ok := reply.(proto.Message); ok {
						if data, marshalErr := proto.Marshal(msg); marshalErr == nil {
							service.responseSize.WithLabelValues("POST", api).Observe(float64(len(data)))
						}
					}
				}

				// Record errors
				if err != nil && service.errorCounter != nil {
					service.errorCounter.WithLabelValues("POST", api, "tracer_error").Inc()
				}
			}

			return reply, err
		}
	}
}
