package http

import (
	"context"
	stdErrors "errors"
	"fmt"
	"time"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/go-kratos/kratos/v2/transport"
	"github.com/go-lynx/lynx/log"
)

type requestLogRedacter interface {
	Redact() string
}

func (h *ServiceHttp) loggingMiddleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (reply interface{}, err error) {
			var (
				kind      string
				operation string
			)

			startTime := time.Now()
			if info, ok := transport.FromServerContext(ctx); ok {
				kind = info.Kind().String()
				operation = info.Operation()
			}

			reply, err = handler(ctx, req)

			keyvals := []any{
				"msg", "http server request completed",
				"kind", "server",
				"component", kind,
				"operation", operation,
				"args", requestLogArgs(req),
				"code", errorCodeForLog(err),
				"latency", time.Since(startTime).Seconds(),
			}
			keyvals = append(keyvals, errorLogFields(err)...)

			if err != nil {
				log.ErrorwCtx(ctx, keyvals...)
			} else {
				log.InfowCtx(ctx, keyvals...)
			}
			return reply, err
		}
	}
}

func requestLogArgs(req interface{}) string {
	if redacter, ok := req.(requestLogRedacter); ok {
		return redacter.Redact()
	}
	if stringer, ok := req.(fmt.Stringer); ok {
		return stringer.String()
	}
	return fmt.Sprintf("%+v", req)
}

func errorCodeForLog(err error) int32 {
	if err == nil {
		return 200
	}
	if se := kratoserrors.FromError(err); se != nil {
		return se.Code
	}
	return 500
}

func errorLogFields(err error) []any {
	if err == nil {
		return nil
	}

	se := kratoserrors.FromError(err)
	fields := []any{
		"error", err.Error(),
		"stack", fmt.Sprintf("%+v", err),
		"error_type", fmt.Sprintf("%T", err),
	}

	if se != nil {
		if se.Reason != "" {
			fields = append(fields, "reason", se.Reason)
		}
		if se.Message != "" {
			fields = append(fields, "error_message", se.Message)
		}
		if len(se.Metadata) > 0 {
			fields = append(fields, "error_metadata", se.Metadata)
		}
	}

	if cause := rootCause(err); cause != nil && cause.Error() != err.Error() {
		fields = append(fields, "cause", cause.Error(), "cause_type", fmt.Sprintf("%T", cause))
	}

	return fields
}

func rootCause(err error) error {
	if err == nil {
		return nil
	}
	for {
		next := stdErrors.Unwrap(err)
		if next == nil {
			return err
		}
		err = next
	}
}
