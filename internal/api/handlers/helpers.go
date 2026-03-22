package handlers

import (
	"encoding/json"
	"io"
)

// errorResponse returns a standard error JSON payload.
func errorResponse(message string) map[string]string {
	return map[string]string{"error": message}
}

// decodeJSON decodes JSON from a reader into v.
func decodeJSON(r io.Reader, v interface{}) error {
	return json.NewDecoder(r).Decode(v)
}
