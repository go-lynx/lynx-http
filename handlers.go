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
		if h.errorCounter != nil {
			h.errorCounter.WithLabelValues(r.Method, r.URL.Path, "not_found").Inc()
		}

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
		if h.errorCounter != nil {
			h.errorCounter.WithLabelValues(r.Method, r.URL.Path, "method_not_allowed").Inc()
		}

		log.Warnf("405 method not allowed: %s %s", r.Method, r.URL.Path)
	})
}

// enhancedErrorEncoder encodes errors to JSON with a numeric "code" in the body.
// If ServiceHttp.ErrorCodeMapper is set, it is used; otherwise the default is Kratos se.Code or 500.
// HTTP status is 200 by default to avoid leaking error details via status; applications may change this.
func (h *ServiceHttp) enhancedErrorEncoder(w http.ResponseWriter, r *http.Request, err error) {
	se := errors.FromError(err)
	var code int
	if h.ErrorCodeMapper != nil {
		code = h.ErrorCodeMapper(se)
	} else {
		code = defaultErrorCode(se)
	}

	if h.errorCounter != nil {
		h.errorCounter.WithLabelValues(r.Method, r.URL.Path, "server_error").Inc()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	response := map[string]interface{}{"code": code}
	data, marshalErr := json.Marshal(response)
	if marshalErr != nil {
		log.Errorf("Failed to encode error response: %v", marshalErr)
		data = []byte(`{"code": 500}`)
	}
	if _, writeErr := w.Write(data); writeErr != nil {
		return
	}
}
