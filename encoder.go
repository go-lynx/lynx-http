// Package http implements HTTP-related features, including response encoding and middleware.
package http

import (
	"encoding/json"
	nhttp "net/http"

	"github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http"
	"github.com/go-lynx/lynx/log"
	"google.golang.org/protobuf/runtime/protoimpl"
)

// Response represents a standardized HTTP response structure.
// It contains the status code, message, and an optional data payload.
type Response struct {
	// state is the status of the protobuf message for internal handling.
	state protoimpl.MessageState
	// sizeCache caches the message size to optimize serialization performance.
	sizeCache protoimpl.SizeCache
	// unknownFields stores unknown fields encountered during parsing.
	unknownFields protoimpl.UnknownFields

	// Code is the response status code.
	Code int `protobuf:"bytes,1,opt,name=code,proto3" json:"code,omitempty"`
	// Message is the descriptive message of the response.
	Message string `protobuf:"bytes,2,opt,name=message,proto3" json:"message,omitempty"`
	// Data is the payload carried by the response.
	Data interface{} `protobuf:"bytes,2,opt,name=data,proto3" json:"data,omitempty"`
}

// ResponseEncoder encodes response data into a standardized JSON format.
// It wraps the data in a Response struct with code=200 and message="success".
// w is the HTTP response writer used to send the response to the client.
// r is the HTTP request object (currently unused).
// data is the response payload to encode.
// Returns an error if encoding fails.
func ResponseEncoder(w http.ResponseWriter, r *http.Request, data interface{}) error {
	// Create a standardized response structure
	res := &Response{
		Code:    200,
		Message: "success",
		Data:    data,
	}
	codec, ok := http.CodecForRequest(r, "Accept")
	if !ok || codec == nil {
		// Fallback to JSON when codec is unavailable or unsupported Accept
		body, marshalErr := json.Marshal(res)
		if marshalErr != nil {
			w.WriteHeader(nhttp.StatusInternalServerError)
			return marshalErr
		}
		w.Header().Set("Content-Type", "application/json")
		_, wErr := w.Write(body)
		return wErr
	}
	body, marshalErr := codec.Marshal(res)
	if marshalErr != nil {
		w.WriteHeader(nhttp.StatusInternalServerError)
		return marshalErr
	}
	// Write the JSON data to the HTTP response
	_, wErr := w.Write(body)
	if wErr != nil {
		return wErr
	}
	return nil
}

// EncodeErrorFunc encodes a Kratos error to a generic JSON response with "code" (Kratos Code or 500).
// It is business-agnostic; for custom codes, use ServiceHttp.ErrorCodeMapper or your own encoder.
func EncodeErrorFunc(w http.ResponseWriter, r *http.Request, err error) {
	se := errors.FromError(err)
	code := defaultErrorCode(se)
	res := &Response{
		Code: code,
	}
	codec, ok := http.CodecForRequest(r, "Accept")
	if !ok || codec == nil {
		body, _ := json.Marshal(res)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(nhttp.StatusOK)
		_, _ = w.Write(body)
		return
	}
	body, marshalErr := codec.Marshal(res)
	if marshalErr != nil {
		w.WriteHeader(nhttp.StatusOK)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// For security, return 200 for all errors to avoid exposing error information in HTTP status
	w.WriteHeader(nhttp.StatusOK)
	_, wErr := w.Write(body)
	if wErr != nil {
		log.Error("write error", wErr)
	}
}
