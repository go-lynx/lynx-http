// Package http implements the HTTP server plugin for the Lynx framework.
// It is business-agnostic: no application-specific error codes or module names.
package http

import (
	"encoding/json"
	"net/http"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-lynx/lynx/log"
)

// defaultErrorCode returns a generic HTTP-style code from Kratos error (se.Code, or 500 if zero).
// Used when no custom ErrorCodeMapper is set on ServiceHttp.
func defaultErrorCode(se *errors.Error) int {
	if se == nil {
		return 500
	}
	if se.Code != 0 {
		return int(se.Code)
	}
	return 500
}

// BodyCodeSystemFailure 与业务约定：响应 JSON 中 code==500 表示系统/未识别错误，对应 HTTP 500；其余 code 一律 HTTP 200。
const BodyCodeSystemFailure = 500

// responseBodyCodeFromError 与 enhancedErrorEncoder 共用：决定写入 body 的 code 数字（供熔断器等同源判断）。
func (h *ServiceHttp) responseBodyCodeFromError(err error) int {
	se := errors.FromError(err)
	if h.ErrorCodeMapper != nil {
		return h.ErrorCodeMapper(se)
	}
	return defaultErrorCode(se)
}

// notFoundHandler returns a 404 handler.
func (h *ServiceHttp) notFoundHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)

		// Only return code, not message, to avoid exposing sensitive information to frontend
		response := map[string]interface{}{
			"code": 404,
			// Message field removed for security reasons
		}

		// Serialize and write the response
		if data, err := json.Marshal(response); err == nil {
			_, writeErr := w.Write(data)
			if writeErr != nil {
				return
			}
		} else {
			log.Errorf("Failed to marshal 404 response: %v", err)
			_, writeErr := w.Write([]byte(`{"code": 404}`))
			if writeErr != nil {
				return
			}
		}

		// Record 404 errors
		h.recordErrorMetric(r.Method, r.URL.Path, "not_found")

		log.Warnf("404 not found: %s %s", r.Method, r.URL.Path)
	})
}

// methodNotAllowedHandler returns a 405 handler.
func (h *ServiceHttp) methodNotAllowedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)

		// Only return code, not message, to avoid exposing sensitive information to frontend
		response := map[string]interface{}{
			"code": 405,
			// Message field removed for security reasons
		}

		// Serialize and write the response
		if data, err := json.Marshal(response); err == nil {
			_, writeErr := w.Write(data)
			if writeErr != nil {
				return
			}
		} else {
			log.Errorf("Failed to marshal 405 response: %v", err)
			_, writeErr := w.Write([]byte(`{"code": 405}`))
			if writeErr != nil {
				return
			}
		}

		// Record 405 errors
		h.recordErrorMetric(r.Method, r.URL.Path, "method_not_allowed")

		log.Warnf("405 method not allowed: %s %s", r.Method, r.URL.Path)
	})
}

// enhancedErrorEncoder 将错误编码为 JSON：body 仅含 {"code":…}。
// 约定：除「系统/未识别」外 HTTP 恒为 200，由 body.code 表达业务（如 100004）；仅当 body.code==BodyCodeSystemFailure(500) 时 HTTP 为 500。
// 这样网关/熔断器不会因业务失败把服务判死；未配置 ErrorCodeMapper 时沿用 defaultErrorCode（多为 Kratos 语义码写入 body，HTTP 仍按上述规则）。
func (h *ServiceHttp) enhancedErrorEncoder(w http.ResponseWriter, r *http.Request, err error) {
	bodyCode := h.responseBodyCodeFromError(err)

	httpStatus := http.StatusOK
	if bodyCode == BodyCodeSystemFailure {
		httpStatus = http.StatusInternalServerError
	}

	kind := "business_error"
	if bodyCode == BodyCodeSystemFailure {
		kind = "server_error"
	}
	h.recordErrorMetric(r.Method, r.URL.Path, kind)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatus)
	response := map[string]interface{}{"code": bodyCode}
	data, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		log.Errorf("Failed to encode error response: %v", marshalErr)
		data = []byte(`{"code": 500}`)
	}
	if _, writeErr := w.Write(data); writeErr != nil {
		return
	}
}
