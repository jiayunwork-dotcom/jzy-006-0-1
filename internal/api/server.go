package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"cosmoredshift/internal/cosmology"
	"cosmoredshift/internal/store"
)

// Server wires the HTTP handlers to persistence.
type Server struct {
	store store.Store
	log   *log.Logger
	mux   *http.ServeMux
	start time.Time
}

// NewServer constructs a Server with all routes registered.
func NewServer(st store.Store, logger *log.Logger) *Server {
	if logger == nil {
		logger = log.Default()
	}
	s := &Server{store: st, log: logger, start: time.Now().UTC()}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health/live", s.handleLiveness)
	mux.HandleFunc("GET /health/ready", s.handleReadiness)
	mux.HandleFunc("POST /api/v1/redshift", s.handleRedshift)
	mux.HandleFunc("POST /api/v1/distance", s.handleDistance)
	mux.HandleFunc("POST /api/v1/batch", s.handleBatch)
	mux.HandleFunc("GET /api/v1/history", s.handleHistory)
	mux.HandleFunc("GET /api/v1/example", s.handleExample)

	s.mux = mux
	return s
}

// Handler returns the root HTTP handler with middleware applied.
func (s *Server) Handler() http.Handler {
	return s.recoverMiddleware(s.requestIDMiddleware(s.logMiddleware(s.mux)))
}

// ---- health ----------------------------------------------------------------

func (s *Server) handleLiveness(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "alive"})
}

func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Printf("readiness check failed: %v", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "not ready", "error": "persistence unavailable",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready", "store": "ok"})
}

// ---- redshift --------------------------------------------------------------

func (s *Server) handleRedshift(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFromContext(r.Context())

	var req RedshiftRequest
	if cerr := decodeStrict(w, r, &req); cerr != nil {
		s.persistError(r.Context(), requestID, store.KindRedshift, req.Redshift,
			req.RestWavelengthNm, req.ObservedWavelengthNm, req.VelocityKmS, nil, req.Relativistic, cerr)
		writeError(w, cerr)
		return
	}

	z, cerr := resolveInput(req)
	if cerr != nil {
		s.persistError(r.Context(), requestID, store.KindRedshift, req.Redshift,
			req.RestWavelengthNm, req.ObservedWavelengthNm, req.VelocityKmS, nil, req.Relativistic, cerr)
		writeError(w, cerr)
		return
	}

	v := cosmology.VelocityFromRedshift(z, req.Relativistic)
	rec := &store.Record{
		RequestID: requestID, Kind: store.KindRedshift,
		RestWavelength: req.RestWavelengthNm, ObservedWavelength: req.ObservedWavelengthNm,
		Redshift: &z, Velocity: &v, Relativistic: req.Relativistic,
		Blueshift: z < 0, Success: true,
	}
	if err := s.store.Save(r.Context(), rec); err != nil {
		s.log.Printf("persist failure: %v", err)
		writeError(w, persistenceError())
		return
	}

	writeJSON(w, http.StatusOK, RedshiftResponse{
		RequestID: requestID, Redshift: z, Classification: cosmology.Classification(z),
		Blueshift: z < 0, VelocityKmS: v, Relation: relationName(req.Relativistic),
		Relativistic: req.Relativistic, Units: fixedUnits,
	})
}

// ---- distance --------------------------------------------------------------

func (s *Server) handleDistance(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFromContext(r.Context())

	var req DistanceRequest
	if cerr := decodeStrict(w, r, &req); cerr != nil {
		s.persistError(r.Context(), requestID, store.KindDistance, req.Redshift,
			req.RestWavelengthNm, req.ObservedWavelengthNm, req.VelocityKmS, nil, req.Relativistic, cerr)
		writeError(w, cerr)
		return
	}

	// Resolve the spectrum-line inputs first so that non-positive
	// wavelengths and ambiguous/missing fields are reported before the
	// (separately required) H0 check.
	z, cerr := resolveInput(req.RedshiftRequest)
	if cerr != nil {
		s.persistError(r.Context(), requestID, store.KindDistance, req.Redshift,
			req.RestWavelengthNm, req.ObservedWavelengthNm, req.VelocityKmS, req.H0KmSMpc,
			req.Relativistic, cerr)
		writeError(w, cerr)
		return
	}
	if req.H0KmSMpc == nil {
		cerr := &cosmology.Error{Code: cosmology.ErrMissingField,
			Message: "h0_km_s_mpc is required (Hubble constant in km/s/Mpc)"}
		s.persistErrorFull(r.Context(), requestID, z, req, nil, cerr)
		writeError(w, cerr)
		return
	}
	h0 := *req.H0KmSMpc
	v := cosmology.VelocityFromRedshift(z, req.Relativistic)

	// Blueshift distance queries are answered with an explicit blueshift
	// marker as a structured error (422); never with a negative fake distance.
	if z < 0 {
		cerr := cosmology.BlueshiftDistanceError(z)
		rec := &store.Record{
			RequestID: requestID, Kind: store.KindDistance,
			RestWavelength: req.RestWavelengthNm, ObservedWavelength: req.ObservedWavelengthNm,
			Redshift: &z, Velocity: &v, HubbleConstant: &h0,
			Relativistic: req.Relativistic, Blueshift: true, Success: false,
			ErrorCode: cerr.Code, ErrorMessage: cerr.Message,
		}
		if err := s.store.Save(r.Context(), rec); err != nil {
			s.log.Printf("persist failure: %v", err)
			writeError(w, persistenceError())
			return
		}
		writeError(w, cerr)
		return
	}

	d, cerr := cosmology.LinearDistance(v, h0)
	if cerr != nil {
		s.persistErrorFull(r.Context(), requestID, z, req, &h0, cerr)
		writeError(w, cerr)
		return
	}

	inRegime := cosmology.InLinearRegime(z)
	rec := &store.Record{
		RequestID: requestID, Kind: store.KindDistance,
		RestWavelength: req.RestWavelengthNm, ObservedWavelength: req.ObservedWavelengthNm,
		Redshift: &z, Velocity: &v, Distance: &d, HubbleConstant: &h0,
		Relativistic: req.Relativistic, LinearRegime: inRegime,
		OutsideLinear: !inRegime, Success: true,
	}
	if err := s.store.Save(r.Context(), rec); err != nil {
		s.log.Printf("persist failure: %v", err)
		writeError(w, persistenceError())
		return
	}

	resp := DistanceResponse{
		RequestID: requestID, Redshift: z, Classification: cosmology.Classification(z),
		VelocityKmS: v, DistanceMpc: &d, H0KmSMpc: &h0,
		Relation: relationName(req.Relativistic), Relativistic: req.Relativistic,
		LinearRegime: inRegime, OutsideLinear: !inRegime, Units: fixedUnits,
	}
	if !inRegime {
		resp.Warning = cosmology.WarningOutsideLinear
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- batch -----------------------------------------------------------------

const maxBatchLines = 1000

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	requestID := requestIDFromContext(r.Context())

	var req BatchRequest
	if cerr := decodeStrict(w, r, &req); cerr != nil {
		writeError(w, cerr)
		return
	}
	if len(req.Lines) == 0 {
		writeError(w, &cosmology.Error{Code: cosmology.ErrMissingField,
			Message: "lines must contain at least one spectrum line"})
		return
	}
	if len(req.Lines) > maxBatchLines {
		writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
			Message: "too many lines (max 1000 per batch)"})
		return
	}

	results := make([]BatchLineResult, len(req.Lines))
	for i := range req.Lines {
		results[i] = s.computeLine(r.Context(), requestID, req.Lines[i], req.H0KmSMpc, req.Relativistic)
	}

	writeJSON(w, http.StatusOK, BatchResponse{
		RequestID: requestID, Count: len(results), Results: results, Units: fixedUnits,
	})
}

// computeLine computes and persists one batch line.
func (s *Server) computeLine(ctx context.Context, requestID string, line BatchLine,
	defaultH0 *float64, relativistic bool) BatchLineResult {
	res := BatchLineResult{LineID: line.LineID}

	rr := RedshiftRequest{
		RestWavelengthNm: line.RestWavelengthNm, ObservedWavelengthNm: line.ObservedWavelengthNm,
		Redshift: line.Redshift, VelocityKmS: line.VelocityKmS, Relativistic: relativistic,
	}
	h0Ptr := line.H0KmSMpc
	if h0Ptr == nil {
		h0Ptr = defaultH0
	}

	z, cerr := resolveInput(rr)
	if cerr != nil {
		s.persistLineError(ctx, requestID, line, h0Ptr, relativistic, cerr)
		res.OK = false
		res.Error = cerr
		return res
	}
	if h0Ptr == nil {
		cerr := &cosmology.Error{Code: cosmology.ErrMissingField,
			Message: "h0_km_s_mpc is required per line or at batch level (km/s/Mpc)"}
		s.persistLineError(ctx, requestID, line, nil, relativistic, cerr)
		res.OK = false
		res.Error = cerr
		return res
	}
	h0 := *h0Ptr
	v := cosmology.VelocityFromRedshift(z, relativistic)

	if z < 0 {
		cerr := cosmology.BlueshiftDistanceError(z)
		rec := &store.Record{
			RequestID: requestID, Kind: store.KindDistance,
			RestWavelength: line.RestWavelengthNm, ObservedWavelength: line.ObservedWavelengthNm,
			Redshift: &z, Velocity: &v, HubbleConstant: &h0, Relativistic: relativistic,
			Blueshift: true, Success: false, ErrorCode: cerr.Code, ErrorMessage: cerr.Message,
		}
		if err := s.store.Save(ctx, rec); err != nil {
			s.log.Printf("persist failure: %v", err)
			res.OK = false
			res.Error = persistenceError()
			return res
		}
		// Explicitly marked as a blueshift error, never a negative distance.
		res.OK = false
		res.Error = cerr
		return res
	}

	d, cerr := cosmology.LinearDistance(v, h0)
	if cerr != nil {
		s.persistLineError(ctx, requestID, line, h0Ptr, relativistic, cerr)
		res.OK = false
		res.Error = cerr
		return res
	}

	inRegime := cosmology.InLinearRegime(z)
	rec := &store.Record{
		RequestID: requestID, Kind: store.KindDistance,
		RestWavelength: line.RestWavelengthNm, ObservedWavelength: line.ObservedWavelengthNm,
		Redshift: &z, Velocity: &v, Distance: &d, HubbleConstant: &h0,
		Relativistic: relativistic, LinearRegime: inRegime, OutsideLinear: !inRegime,
		Success: true,
	}
	if err := s.store.Save(ctx, rec); err != nil {
		s.log.Printf("persist failure: %v", err)
		res.OK = false
		res.Error = persistenceError()
		return res
	}

	resp := &DistanceResponse{
		RequestID: requestID, Redshift: z, Classification: cosmology.Classification(z),
		VelocityKmS: v, DistanceMpc: &d, H0KmSMpc: &h0,
		Relation: relationName(relativistic), Relativistic: relativistic,
		LinearRegime: inRegime, OutsideLinear: !inRegime, Units: fixedUnits,
	}
	if !inRegime {
		resp.Warning = cosmology.WarningOutsideLinear
	}
	res.OK = true
	res.Result = resp
	return res
}

// ---- history ---------------------------------------------------------------

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.HistoryFilter{
		RequestID: q.Get("request_id"),
		Kind:      q.Get("kind"),
	}
	switch q.Get("kind") {
	case "", store.KindRedshift, store.KindDistance:
	default:
		writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
			Message: "kind must be 'redshift' or 'distance'"})
		return
	}
	if v := q.Get("blueshift"); v != "" {
		b, ok := parseBool(v)
		if !ok {
			writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
				Message: "blueshift must be true or false"})
			return
		}
		f.Blueshift = &b
	}
	if v := q.Get("outside_linear"); v != "" {
		b, ok := parseBool(v)
		if !ok {
			writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
				Message: "outside_linear must be true or false"})
			return
		}
		f.OutsideLinear = &b
	}
	if v := q.Get("success"); v != "" {
		b, ok := parseBool(v)
		if !ok {
			writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
				Message: "success must be true or false"})
			return
		}
		f.Success = &b
	}
	var err error
	if f.Limit, err = parsePositiveInt(q.Get("limit"), 100); err != nil {
		writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter, Message: err.Error()})
		return
	}
	if f.Offset, err = parseNonNegativeInt(q.Get("offset")); err != nil {
		writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter, Message: err.Error()})
		return
	}
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
				Message: "from must be RFC3339, e.g. 2026-09-18T00:00:00Z"})
			return
		}
		f.From = &t
	}
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeError(w, &cosmology.Error{Code: cosmology.ErrInvalidQueryParameter,
				Message: "to must be RFC3339, e.g. 2026-09-18T00:00:00Z"})
			return
		}
		f.To = &t
	}

	items, err := s.store.Query(r.Context(), f)
	if err != nil {
		s.log.Printf("history query failure: %v", err)
		writeError(w, persistenceError())
		return
	}
	writeJSON(w, http.StatusOK, HistoryResponse{Count: len(items), Items: items})
}

// ---- preset worked example -------------------------------------------------

// handleExample returns a ready-to-call worked example: a ~3% redshift line
// (500.0 nm -> 515.0 nm) with H0 = 70 km/s/Mpc.
// Linear: z=0.03, v=8993.77374 km/s, d≈128.482 Mpc.
func (s *Server) handleExample(w http.ResponseWriter, _ *http.Request) {
	const rest, obs, h0 = 500.0, 515.0, 70.0
	z, _ := cosmology.RedshiftFromWavelengths(rest, obs)
	vLin := cosmology.LinearVelocity(z)
	dLin, _ := cosmology.LinearDistance(vLin, h0)
	vRel := cosmology.RelativisticVelocity(z)
	dRel, _ := cosmology.LinearDistance(vRel, h0)
	writeJSON(w, http.StatusOK, map[string]any{
		"description": "worked example: 500.0 nm line observed at 515.0 nm, H0=70 km/s/Mpc",
		"request": map[string]any{
			"rest_wavelength_nm": rest, "observed_wavelength_nm": obs,
			"h0_km_s_mpc": h0, "relativistic": false,
		},
		"linear_expected": map[string]any{
			"redshift": z, "velocity_km_s": vLin, "distance_mpc": dLin,
		},
		"relativistic_expected": map[string]any{
			"redshift": z, "velocity_km_s": vRel, "distance_mpc": dRel,
		},
		"units": fixedUnits,
	})
}

// ---- persistence helpers ---------------------------------------------------

func (s *Server) persistError(ctx context.Context, requestID, kind string,
	zInput, rest, obs, vel *float64, h0 *float64, relativistic bool, cerr *cosmology.Error) {
	z := derefOrNil(zInput)
	s.saveFailure(ctx, requestID, kind, rest, obs, z, vel, h0, relativistic, z != nil && *z < 0, cerr)
}

func (s *Server) persistErrorFull(ctx context.Context, requestID string, z float64,
	req DistanceRequest, h0 *float64, cerr *cosmology.Error) {
	zp := z
	rec := &store.Record{
		RequestID: requestID, Kind: store.KindDistance,
		RestWavelength: req.RestWavelengthNm, ObservedWavelength: req.ObservedWavelengthNm,
		Redshift: &zp, Velocity: velPtr(z, req.Relativistic), HubbleConstant: h0,
		Relativistic: req.Relativistic, Blueshift: z < 0, Success: false,
		ErrorCode: cerr.Code, ErrorMessage: cerr.Message,
	}
	_ = s.store.Save(ctx, rec)
}

func (s *Server) persistLineError(ctx context.Context, requestID string, line BatchLine,
	h0 *float64, relativistic bool, cerr *cosmology.Error) {
	var zp *float64
	if line.Redshift != nil {
		z := *line.Redshift
		zp = &z
	}
	s.saveFailure(ctx, requestID, store.KindDistance, line.RestWavelengthNm,
		line.ObservedWavelengthNm, zp, line.VelocityKmS, h0, relativistic,
		zp != nil && *zp < 0, cerr)
}

func (s *Server) saveFailure(ctx context.Context, requestID, kind string,
	rest, obs, z, vel, h0 *float64, relativistic, blueshift bool, cerr *cosmology.Error) {
	var v *float64
	if z != nil {
		vv := cosmology.VelocityFromRedshift(*z, relativistic)
		v = &vv
	}
	rec := &store.Record{
		RequestID: requestID, Kind: kind, RestWavelength: rest, ObservedWavelength: obs,
		Redshift: z, Velocity: firstNonNil(v, vel), HubbleConstant: h0,
		Relativistic: relativistic, Blueshift: blueshift, Success: false,
		ErrorCode: cerr.Code, ErrorMessage: cerr.Message,
	}
	if err := s.store.Save(ctx, rec); err != nil {
		s.log.Printf("persist failure: %v", err)
	}
}

// ---- small helpers ---------------------------------------------------------

func persistenceError() *cosmology.Error {
	return &cosmology.Error{Code: "PERSISTENCE_ERROR",
		Message: "failed to persist accounting record"}
}

func relationName(relativistic bool) string {
	if relativistic {
		return "relativistic_doppler"
	}
	return "cosmological_linear"
}

func derefOrNil(p *float64) *float64 {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func velPtr(z float64, relativistic bool) *float64 {
	v := cosmology.VelocityFromRedshift(z, relativistic)
	return &v
}

func firstNonNil(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

// statusFor maps structured physics codes to HTTP status codes.
func statusFor(code string) int {
	switch code {
	case cosmology.ErrBlueshiftDistance:
		return http.StatusUnprocessableEntity
	case "PERSISTENCE_ERROR":
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

func writeError(w http.ResponseWriter, cerr *cosmology.Error) {
	writeJSON(w, statusFor(cerr.Code), apiError{Error: *cerr})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is exceptional; fall back to time.
		return time.Now().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
