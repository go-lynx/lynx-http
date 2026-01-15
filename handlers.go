// Package http implements the HTTP server plugin for the Lynx framework.
package http

import (
	"encoding/json"
	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-lynx/lynx/log"
	"net/http"
	"strings"
)

// BusinessCodeMapper maps ErrorReason to business code based on module base
// Module base codes:
//   - 100000-199999: User module (betday-user)
//   - 200000-299999: Game module (betday-game)
//   - 300000-399999: Payment module (betday-pay)
//   - 400000-499999: Base module (betday-base)
//
// Business code = module base + ErrorReason enum value
func BusinessCodeMapper(reason string, moduleBase int) int {
	// Common ErrorReason enum value mappings
	// These are the standard enum values from error_reason.proto
	errorReasonMap := map[string]int{
		"USER_DOES_NOT_EXIST":       0,
		"INCORRECT_PASSWORD":        1,
		"ACCOUNT_HAS_BEEN_BANNED":   2,
		"LOGIN_ERROR":               3,
		"USER_ALREADY_EXISTS":       4,
		"INVALID_VERIFICATION_CODE": 5,
		"VERIFICATION_CODE_EXPIRED": 6,
		"INVALID_TOKEN":             7,
		"TOKEN_EXPIRED":             8,
		"REGISTER_ERROR":            9,
		"UPDATE_PROFILE_ERROR":      10,
		"SEND_CODE_ERROR":           11,
	}

	// Extract ErrorReason from reason string (format: "layout.login.v1.ErrorReason_USER_DOES_NOT_EXIST" or "USER_DOES_NOT_EXIST")
	reasonUpper := strings.ToUpper(reason)

	// Try to find the ErrorReason enum name in the reason string
	for enumName, enumValue := range errorReasonMap {
		if strings.Contains(reasonUpper, enumName) {
			return moduleBase + enumValue
		}
	}

	// If not found in common mappings, try to extract enum value from reason string
	// Reason format might be: "layout.login.v1.ErrorReason_USER_DOES_NOT_EXIST"
	// or just "USER_DOES_NOT_EXIST"
	parts := strings.Split(reasonUpper, "_")
	if len(parts) > 0 {
		// Try to find a numeric suffix or use hash-based mapping
		// For unknown reasons, return a generic error code
		return moduleBase + 999 // Generic error code for unknown reasons
	}

	// Default: return module base + 999 for unknown errors
	return moduleBase + 999
}

// detectModuleBase detects the module base code from error reason
// This is a fallback mechanism when module base is not explicitly configured
// The reason string typically contains the package path, e.g., "layout.login.v1.ErrorReason_USER_DOES_NOT_EXIST"
// We can infer the module from the package path or service context
func detectModuleBase(reason string) int {
	reasonLower := strings.ToLower(reason)

	// Detect module from package path in reason string
	// Common patterns:
	// - "betday-user" or "user" in package path -> User module (100000)
	// - "betday-game" or "game" in package path -> Game module (200000)
	// - "betday-pay" or "pay" in package path -> Payment module (300000)
	// - "betday-base" or "base" in package path -> Base module (400000)

	// Check for explicit service name in reason
	if strings.Contains(reasonLower, "betday-user") ||
		(strings.Contains(reasonLower, "user") && !strings.Contains(reasonLower, "game") && !strings.Contains(reasonLower, "pay")) {
		return 100000 // User module (100000-199999)
	} else if strings.Contains(reasonLower, "betday-game") || strings.Contains(reasonLower, "game") {
		return 200000 // Game module (200000-299999)
	} else if strings.Contains(reasonLower, "betday-pay") || strings.Contains(reasonLower, "pay") || strings.Contains(reasonLower, "payment") {
		return 300000 // Payment module (300000-399999)
	} else if strings.Contains(reasonLower, "betday-base") || strings.Contains(reasonLower, "base") {
		return 400000 // Base module (400000-499999)
	}

	// Default to user module if cannot detect
	// This ensures backward compatibility
	return 100000
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

// enhancedErrorEncoder is an enhanced error encoder.
// It returns business code in response body, not HTTP status code.
// HTTP status code is set to 200 for all errors to avoid exposing error information.
func (h *ServiceHttp) enhancedErrorEncoder(w http.ResponseWriter, r *http.Request, err error) {
	// Convert the error to a Kratos Error entity to extract the error reason
	se := errors.FromError(err)

	// Detect module base code from error reason or use default
	moduleBase := detectModuleBase(se.Reason)

	// Map ErrorReason to business code
	businessCode := BusinessCodeMapper(se.Reason, moduleBase)

	// Determine HTTP status code based on error type
	// For security, we can return 200 for all errors, or use the original HTTP code
	// Here we use 200 to avoid exposing error information in HTTP status
	httpStatusCode := http.StatusOK

	// Alternatively, you can use the original HTTP status code for proper HTTP semantics:
	// if se.Code > 0 && se.Code >= 400 && se.Code < 600 {
	//     httpStatusCode = int(se.Code)
	// }

	// Record error metrics
	if h.errorCounter != nil {
		h.errorCounter.WithLabelValues(r.Method, r.URL.Path, "server_error").Inc()
	}

	// Encode error response
	// Only return business code, not message or error details, to avoid exposing sensitive information to frontend
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(httpStatusCode)
	response := map[string]interface{}{
		"code": businessCode,
		// Error and message fields removed for security reasons
	}
	if data, err := json.Marshal(response); err == nil {
		_, writeErr := w.Write(data)
		if writeErr != nil {
			return
		}
	} else {
		log.Errorf("Failed to encode error response: %v", err)
		_, writeErr := w.Write([]byte(`{"code": 100999}`))
		if writeErr != nil {
			return
		}
	}
}
