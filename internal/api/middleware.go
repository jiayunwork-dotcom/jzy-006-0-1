package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"cosmoredshift/internal/cosmology"
)

type ctxKey string

const requestIDKey ctxKey = "request_id"

func requestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok && v != "" {
		return v
	}
	return "-"
}

// requestIDMiddleware assigns every request a correlation id used in logs
// and in persisted records (so concurrent requests can never be confused).
func (s *Server) requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" {
			id = newRequestID()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		s.log.Printf("%s %s %d %s req_id=%s",
			r.Method, r.URL.Path, rw.status, time.Since(start), requestIDFromContext(r.Context()))
	})
}

// recoverMiddleware turns panics into structured 500 responses so one bad
// request cannot crash concurrent processing.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("panic recovered: %v req_id=%s", rec, requestIDFromContext(r.Context()))
				writeError(w, &cosmology.Error{Code: "INTERNAL_ERROR",
					Message: fmt.Sprintf("internal server error: %v", rec)})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// decodeStrict parses JSON with unknown fields and trailing data rejected,
// and enforces a bounded body.
func decodeStrict(w http.ResponseWriter, r *http.Request, dst any) *cosmology.Error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if err == io.EOF {
			return &cosmology.Error{Code: "INVALID_JSON", Message: "request body is empty"}
		}
		return &cosmology.Error{Code: "INVALID_JSON", Message: "invalid JSON: " + err.Error()}
	}
	if dec.More() {
		return &cosmology.Error{Code: "INVALID_JSON", Message: "request body must contain a single JSON object"}
	}
	return nil
}

func parseBool(v string) (bool, bool) {
	switch v {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	default:
		return false, false
	}
}

func parsePositiveInt(v string, def int) (int, error) {
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("must be a positive integer")
	}
	return n, nil
}

func parseNonNegativeInt(v string) (int, error) {
	if v == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("must be a non-negative integer")
	}
	return n, nil
}
