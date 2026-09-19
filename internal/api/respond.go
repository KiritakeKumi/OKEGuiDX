package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/KiritakeKumi/OKEGuiDX/internal/okerr"
	"github.com/KiritakeKumi/OKEGuiDX/internal/profile"
)

// errorBody is the one error shape this API returns. It is the JSON form of
// okerr.Error restricted to the three fields the Web UI needs: a stable title
// to show, an explanation to expand, and the offending field to highlight.
//
// The whole body is {"error": {...}}, so a success payload can never be
// mistaken for a failure one, whatever its shape.
type errorBody struct {
	Error errorInfo `json:"error"`
}

// errorInfo mirrors the summary/detail/field triple of okerr.Error and
// profile.ValidationError.
type errorInfo struct {
	Summary string `json:"summary"`
	Detail  string `json:"detail"`
	// Field is omitted when no single field is to blame, which is the common
	// case for an internal failure.
	Field string `json:"field,omitempty"`
}

// maxErrorDetail bounds the detail string of an error that came from outside
// this program. A tool's stderr can be arbitrarily long, and an API response
// should not be.
const maxErrorDetail = 2000

// writeJSON writes v as the response body with the given status. The body is
// serialized before the header is written, so a value that cannot be encoded
// still produces a well-formed error instead of a truncated 200.
func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			okerr.Wrap(err, okerr.KindUnknown, "无法序列化响应", "%v", err))
		return
	}
	data = append(data, '\n')
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

// writeError writes the standard error object with an explicit status.
//
// Every failure goes through here, so the wire format is defined once. Handlers
// choose the status (400 for a bad request, 409 for a conflict, ...) and pass
// the error value through; errorStatus covers the cases where the status
// follows from the error kind instead.
func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorBody{Error: toErrorInfo(err)})
}

// toErrorInfo maps any error onto the wire format. Both error types that carry
// a summary and a detail are recognized: profile.ValidationError is not an
// okerr.Error, and it is the type every profile problem arrives as.
func toErrorInfo(err error) errorInfo {
	if err == nil {
		return errorInfo{Summary: "未知错误"}
	}

	var ve *profile.ValidationError
	if errors.As(err, &ve) {
		return errorInfo{
			Summary: ve.Summary,
			Detail:  clampDetail(ve.Detail),
			Field:   ve.Field,
		}
	}

	e := okerr.AsError(err)
	return errorInfo{
		Summary: e.Summary,
		Detail:  clampDetail(e.Detail),
	}
}

// clampDetail truncates an over-long detail string, keeping the beginning,
// which is where a tool's message usually is.
//
// The cut is moved back to a rune boundary: a detail is usually a tool's output
// and may contain non-ASCII text, and slicing mid-rune would make the JSON
// encoder replace the broken bytes with U+FFFD.
func clampDetail(s string) string {
	if len(s) <= maxErrorDetail {
		return s
	}
	cut := maxErrorDetail
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

// errorStatus is the HTTP status an error kind maps to. It is used where a
// handler would otherwise repeat the same switch: a validation error is a 400,
// a missing resource a 404, and anything unrecognised a 500.
//
// A failure to read or write the settings is deliberately not routed through
// here: both are server-side problems whatever their kind, so the config
// handlers answer 500 themselves.
func errorStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var ve *profile.ValidationError
	if errors.As(err, &ve) {
		return http.StatusBadRequest
	}
	switch okerr.AsError(err).Kind {
	case okerr.KindNotFound:
		return http.StatusNotFound
	case okerr.KindConfig, okerr.KindUnsupported, okerr.KindMismatch:
		return http.StatusBadRequest
	case okerr.KindCanceled:
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

// decodeBody reads a JSON request body into v.
//
// The reader is bounded (a profile is a few kilobytes) and unknown fields are
// rejected, because a typo in a PATCH body would otherwise be accepted
// silently and do nothing.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return badRequest(err)
	}
	// A second value in the body means the caller sent something other than
	// what this endpoint accepts.
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return badRequest(errors.New("请求体必须是单个 JSON 值"))
	}
	return nil
}

// badRequest wraps a decoding failure in the structured form.
func badRequest(err error) error {
	return okerr.Wrap(err, okerr.KindConfig, "请求内容不合法", "%v", err)
}
