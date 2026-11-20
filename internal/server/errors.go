package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/lazyoracle/mail-export-viewer/internal/index"
	"github.com/lazyoracle/mail-export-viewer/internal/mail"
	"github.com/lazyoracle/mail-export-viewer/internal/source"
)

// Error codes (the "code" field of an error response; see docs/api.md).
const (
	codeBadRequest    = "bad_request"
	codeNotFound      = "not_found"
	codeForbiddenHost = "forbidden_host"
	codeMethod        = "method_not_allowed"
	codeURITooLong    = "uri_too_long"
	codeGone          = "gone"
	codeStale         = "stale"
	codeTooLarge      = "too_large"
	codeUnavailable   = "unavailable"
	codeBusy          = "busy"
	codeInternal      = "internal"
)

// writeJSON writes v with status 200.
func (s *Server) writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		s.log.Printf("server: encode response: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Write(append(b, '\n'))
}

// writeError writes {"error": msg, "code": code}. msg must never contain a
// filesystem path or other internal detail.
func writeError(w http.ResponseWriter, status int, code, msg string) {
	h := w.Header()
	h.Del("Content-Disposition")
	h.Del("Content-Length")
	h.Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	b, _ := json.Marshal(map[string]string{"error": msg, "code": code})
	w.Write(append(b, '\n'))
}

// apiError carries an HTTP status, code and client-safe message, and for
// 503 responses the Retry-After seconds (0: none).
type apiError struct {
	status     int
	code, msg  string
	retryAfter int
}

func (e *apiError) Error() string { return e.msg }

func badRequest(msg string) error {
	return &apiError{status: http.StatusBadRequest, code: codeBadRequest, msg: msg}
}

// errBusy answers a request that could not get load budget in time.
var errBusy = &apiError{status: http.StatusServiceUnavailable, code: codeBusy, retryAfter: 5,
	msg: "the viewer is busy reading other large messages; try again shortly"}

var errSourceGone = &apiError{status: http.StatusGone, code: codeGone,
	msg: "the file this message came from is not available any more; reindex to refresh"}

// fail maps an error from the index, a source or a parameter check to a
// response. Unexpected errors are logged (they may mention paths) and
// reported generically.
func (s *Server) fail(w http.ResponseWriter, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		if ae.retryAfter > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(ae.retryAfter))
		}
		writeError(w, ae.status, ae.code, ae.msg)
	case errors.Is(err, index.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "message not found (the index may have been rebuilt)")
	case errors.Is(err, index.ErrBadCursor):
		writeError(w, http.StatusBadRequest, codeBadRequest, "invalid cursor")
	case errors.Is(err, mail.ErrNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "attachment not found")
	case errors.Is(err, source.ErrStale):
		writeError(w, http.StatusGone, codeStale, "the file has changed since it was indexed; reindex to refresh")
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, errSourceGone.status, errSourceGone.code, errSourceGone.msg)
	case errors.Is(err, source.ErrTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, codeTooLarge, "this message is too large to open in the viewer (over 256 MiB)")
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable, codeUnavailable, "request cancelled")
	default:
		s.log.Printf("server: %v", err)
		writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
	}
}
