// Package common holds the response envelope, business error codes and the typed error
// taxonomy that drives HTTP status mapping and task retry decisions.
package common

import (
	"bytes"
	"encoding/json"
)

// Result is the unified {code,message,data} envelope returned by every endpoint.
type Result struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

// SuccessCode is the envelope code of every successful response.
const SuccessCode = 0

// Ok wraps data in a success envelope.
func Ok(data any) Result { return Result{Code: SuccessCode, Message: "success", Data: data} }

// OkVoid is a success envelope with null data.
func OkVoid() Result { return Ok(nil) }

// Fail builds an error envelope from an ErrorCode.
func Fail(code ErrorCode, message string) Result {
	return Result{Code: code.Code, Message: message, Data: nil}
}

// MarshalNoEscape encodes v without HTML escaping (goals and markdown contain <, > and &) and
// without the trailing newline json.Encoder adds.
func MarshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
