package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"sync"
	"testing"

	"cosmoredshift/internal/store"
)

// ---- test harness ----------------------------------------------------------

type testEnv struct {
	t    *testing.T
	addr string
	st   store.Store
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st := store.NewMemoryStore()
	srv := NewServer(st, log.New(io.Discard, "", 0))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = http.Serve(ln, srv.Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	return &testEnv{t: t, addr: "http://" + ln.Addr().String(), st: st}
}

func (e *testEnv) do(method, path string, body any) (int, map[string]any) {
	e.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			e.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.addr+path, rdr)
	if err != nil {
		e.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("do %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			e.t.Fatalf("decode %s: %v (body=%s)", path, err, raw)
		}
	}
	return resp.StatusCode, out
}

func num(m map[string]any, key string) float64 {
	v, ok := m[key].(float64)
	if !ok {
		panic(fmt.Sprintf("key %q missing or not number in %v", key, m))
	}
	return v
}

func asMap(m map[string]any, key string) map[string]any {
	v, ok := m[key].(map[string]any)
	if !ok {
		panic(fmt.Sprintf("key %q missing or not object in %v", key, m))
	}
	return v
}

func closeFloats(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// ---- redshift definition ---------------------------------------------------

// Doubling the observed wavelength through the HTTP API must give 2z+1.
func TestAPIRedshiftDoublingDefinition(t *testing.T) {
	e := newTestEnv(t)

	st1, b1 := e.do("POST", "/api/v1/redshift", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 515,
	})
	if st1 != http.StatusOK {
		t.Fatalf("status=%d body=%v", st1, b1)
	}
	z1 := num(b1, "redshift")
	if !closeFloats(z1, 0.03, 1e-12) {
		t.Fatalf("z1=%v want 0.03", z1)
	}

	st2, b2 := e.do("POST", "/api/v1/redshift", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 1030,
	})
	if st2 != http.StatusOK {
		t.Fatalf("status=%d body=%v", st2, b2)
	}
	z2 := num(b2, "redshift")
	if !closeFloats(z2, 2*z1+1, 1e-12) {
		t.Fatalf("z2=%v want 2*z1+1=%v", z2, 2*z1+1)
	}
	if closeFloats(z2, 2*z1, 1e-12) {
		t.Fatalf("z2 must not be a naive doubling of z1")
	}
}

// ---- distance vs H0 --------------------------------------------------------

func TestAPIDistanceHalvesWhenH0Doubles(t *testing.T) {
	e := newTestEnv(t)

	_, b1 := e.do("POST", "/api/v1/distance", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 515, "h0_km_s_mpc": 70,
	})
	d1 := num(b1, "distance_mpc")
	_, b2 := e.do("POST", "/api/v1/distance", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 515, "h0_km_s_mpc": 140,
	})
	d2 := num(b2, "distance_mpc")
	if !closeFloats(d2, d1/2, 1e-9) {
		t.Fatalf("d2=%v want %v", d2, d1/2)
	}
	if b1["linear_regime"] != true || b1["outside_linear"] != false {
		t.Fatalf("z=0.03 must be inside linear regime: %v %v", b1["linear_regime"], b1["outside_linear"])
	}
}

// ---- zero redshift ---------------------------------------------------------

func TestAPIZeroRedshiftGivesZeroVelocityAndDistance(t *testing.T) {
	e := newTestEnv(t)

	st, b := e.do("POST", "/api/v1/distance", map[string]any{
		"rest_wavelength_nm": 486.1, "observed_wavelength_nm": 486.1, "h0_km_s_mpc": 70,
	})
	if st != http.StatusOK {
		t.Fatalf("status=%d body=%v", st, b)
	}
	if num(b, "redshift") != 0 {
		t.Fatalf("redshift=%v want 0", b["redshift"])
	}
	if num(b, "velocity_km_s") != 0 {
		t.Fatalf("velocity=%v want 0", b["velocity_km_s"])
	}
	if num(b, "distance_mpc") != 0 {
		t.Fatalf("distance=%v want 0", b["distance_mpc"])
	}
	if b["linear_regime"] != true {
		t.Fatalf("z=0 must be in the linear regime")
	}
}

// ---- beyond linear threshold ----------------------------------------------

func TestAPIOutsideLinearThresholdFlagged(t *testing.T) {
	e := newTestEnv(t)
	// z = 0.2 > 0.1
	st, b := e.do("POST", "/api/v1/distance", map[string]any{
		"rest_wavelength_nm": 100, "observed_wavelength_nm": 120, "h0_km_s_mpc": 70,
	})
	if st != http.StatusOK {
		t.Fatalf("status=%d body=%v", st, b)
	}
	if !closeFloats(num(b, "redshift"), 0.2, 1e-12) {
		t.Fatalf("z=%v want 0.2", b["redshift"])
	}
	if b["linear_regime"] != false || b["outside_linear"] != true {
		t.Fatalf("z=0.2 must be flagged outside linear regime")
	}
	warn, _ := b["warning"].(string)
	if warn == "" {
		t.Fatalf("outside-linear result must carry an explicit warning")
	}
	// linear approximation numbers are still present but marked unreliable
	if num(b, "distance_mpc") <= 0 {
		t.Fatalf("linear approximation value should still be returned")
	}
}

// ---- blueshift must never become a negative forward distance ---------------

func TestAPIBlueshiftMarkedNotNegativeDistance(t *testing.T) {
	e := newTestEnv(t)

	// via redshift endpoint: negative z, negative velocity, clearly labelled
	st, b := e.do("POST", "/api/v1/redshift", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 490,
	})
	if st != http.StatusOK || b["blueshift"] != true || b["classification"] != "blueshift" {
		t.Fatalf("blueshift redshift call: status=%d body=%v", st, b)
	}
	if num(b, "redshift") >= 0 {
		t.Fatalf("redshift must be negative for a blueshift")
	}

	// via distance endpoint: structured 422 rejection with an explicit
	// blueshift marker, no distance_mpc anywhere.
	st, b = e.do("POST", "/api/v1/distance", map[string]any{
		"rest_wavelength_nm": 500, "observed_wavelength_nm": 490, "h0_km_s_mpc": 70,
	})
	if st != http.StatusUnprocessableEntity {
		t.Fatalf("blueshift distance status=%d want 422, body=%v", st, b)
	}
	em := asMap(b, "error")
	if em["code"] != "BLUESHIFT_DISTANCE_QUERY" {
		t.Fatalf("error code=%v", em["code"])
	}
	if em["blueshift"] != true {
		t.Fatalf("error must carry explicit blueshift marker: %v", em)
	}
	if rz, ok := em["redshift"].(float64); !ok || rz >= 0 {
		t.Fatalf("error must carry the negative redshift value, got %v", em["redshift"])
	}
	if _, present := b["distance_mpc"]; present {
		t.Fatalf("blueshift distance response must not carry a distance value")
	}

	// direct negative redshift input must behave the same
	st, b = e.do("POST", "/api/v1/distance", map[string]any{
		"redshift": -0.02, "h0_km_s_mpc": 70,
	})
	if st != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%v", st, b)
	}
	if asMap(b, "error")["code"] != "BLUESHIFT_DISTANCE_QUERY" {
		t.Fatalf("negative z must be a blueshift distance error")
	}
	if asMap(b, "error")["blueshift"] != true {
		t.Fatalf("negative z error must be explicitly marked blueshift")
	}
}

// ---- relation switch -------------------------------------------------------

func TestAPIRelativisticSwitchNotConflated(t *testing.T) {
	e := newTestEnv(t)
	body := map[string]any{"redshift": 0.03, "h0_km_s_mpc": 70}

	_, lin := e.do("POST", "/api/v1/distance", body)
	if lin["relation"] != "cosmological_linear" || lin["relativistic"] != false {
		t.Fatalf("default must be cosmological linear: %v %v", lin["relation"], lin["relativistic"])
	}
	if !closeFloats(num(lin, "velocity_km_s"), 299792.458*0.03, 1e-6) {
		t.Fatalf("default velocity=%v want c*z", lin["velocity_km_s"])
	}

	relBody := map[string]any{"redshift": 0.03, "h0_km_s_mpc": 70, "relativistic": true}
	_, rel := e.do("POST", "/api/v1/distance", relBody)
	if rel["relation"] != "relativistic_doppler" || rel["relativistic"] != true {
		t.Fatalf("switch must select relativistic Doppler")
	}
	z := 0.03
	wantV := 299792.458 * ((1+z)*(1+z) - 1) / ((1+z)*(1+z) + 1)
	if !closeFloats(num(rel, "velocity_km_s"), wantV, 1e-6) {
		t.Fatalf("relativistic velocity=%v want %v", rel["velocity_km_s"], wantV)
	}
	if num(rel, "velocity_km_s") == num(lin, "velocity_km_s") {
		t.Fatalf("the two relations must not produce the same velocity")
	}

	// velocity input is allowed under BOTH relations, but each inverts its
	// own formula (the relations must never be conflated).
	st, b := e.do("POST", "/api/v1/redshift", map[string]any{"velocity_km_s": 1000})
	if st != http.StatusOK {
		t.Fatalf("linear velocity input should be accepted: %d %v", st, b)
	}
	if !closeFloats(num(b, "redshift"), 1000/299792.458, 1e-12) {
		t.Fatalf("linear inverse z=%v want v/c", b["redshift"])
	}
	if b["relation"] != "cosmological_linear" {
		t.Fatalf("default relation must be cosmological_linear, got %v", b["relation"])
	}
	// same velocity through the relativistic switch must give a different z
	st, b = e.do("POST", "/api/v1/redshift", map[string]any{"velocity_km_s": wantV, "relativistic": true})
	if st != http.StatusOK {
		t.Fatalf("status=%d body=%v", st, b)
	}
	if !closeFloats(num(b, "redshift"), 0.03, 1e-9) {
		t.Fatalf("relativistic inverse Doppler redshift=%v want 0.03", b["redshift"])
	}
	zLinWant := wantV / 299792.458
	st2, b2 := e.do("POST", "/api/v1/redshift", map[string]any{"velocity_km_s": wantV})
	if st2 != http.StatusOK || !closeFloats(num(b2, "redshift"), zLinWant, 1e-9) {
		t.Fatalf("linear inverse z: status=%d body=%v want %v", st2, b2, zLinWant)
	}
	if closeFloats(num(b, "redshift"), num(b2, "redshift"), 1e-12) {
		t.Fatalf("the two inverse relations must not produce the same z")
	}
	// superluminal velocity is invalid only for the relativistic inversion
	st, b = e.do("POST", "/api/v1/redshift", map[string]any{"velocity_km_s": 400000, "relativistic": true})
	if st != http.StatusBadRequest || asMap(b, "error")["code"] != "INVALID_NUMBER" {
		t.Fatalf("v>=c must be rejected under relativistic switch: %d %v", st, b)
	}
}

// ---- structured errors -----------------------------------------------------

func TestAPIStructuredErrors(t *testing.T) {
	e := newTestEnv(t)

	cases := []struct {
		name string
		body map[string]any
		code string
	}{
		{"non-positive rest wavelength", map[string]any{"rest_wavelength_nm": 0, "observed_wavelength_nm": 500}, "NON_POSITIVE_REST_WAVELENGTH"},
		{"negative observed wavelength", map[string]any{"rest_wavelength_nm": 500, "observed_wavelength_nm": -1}, "NON_POSITIVE_OBSERVED_WAVELENGTH"},
		{"non-positive H0", map[string]any{"redshift": 0.03, "h0_km_s_mpc": -70}, "NON_POSITIVE_HUBBLE_CONSTANT"},
		{"zero H0", map[string]any{"redshift": 0.03, "h0_km_s_mpc": 0}, "NON_POSITIVE_HUBBLE_CONSTANT"},
		{"missing everything", map[string]any{}, "MISSING_FIELD"},
		{"wavelength half provided", map[string]any{"rest_wavelength_nm": 500}, "MISSING_FIELD"},
		{"ambiguous z and wavelengths", map[string]any{"rest_wavelength_nm": 500, "observed_wavelength_nm": 510, "redshift": 0.02}, "AMBIGUOUS_INPUT"},
		{"distance missing H0", map[string]any{"redshift": 0.03}, "MISSING_FIELD"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, b := e.do("POST", "/api/v1/distance", c.body)
			if st == http.StatusOK {
				t.Fatalf("expected error, got 200: %v", b)
			}
			if got := asMap(b, "error")["code"]; got != c.code {
				t.Fatalf("code=%v want %s (body=%v)", got, c.code, b)
			}
		})
	}

	st, b := e.do("POST", "/api/v1/distance", map[string]any{"redshift": 0.03, "h0_km_s_mpc": 70, "bogus": 1})
	if st != http.StatusBadRequest || asMap(b, "error")["code"] != "INVALID_JSON" {
		t.Fatalf("unknown fields must be rejected: %d %v", st, b)
	}
}

// ---- worked example endpoint ----------------------------------------------

func TestAPIWorkedExample(t *testing.T) {
	e := newTestEnv(t)
	st, b := e.do("GET", "/api/v1/example", nil)
	if st != http.StatusOK {
		t.Fatalf("status=%d body=%v", st, b)
	}
	lin := asMap(b, "linear_expected")
	if !closeFloats(num(lin, "redshift"), 0.03, 1e-12) {
		t.Fatalf("example z=%v want 0.03", lin["redshift"])
	}
	if !closeFloats(num(lin, "distance_mpc"), 128.482482, 1e-4) {
		t.Fatalf("example distance=%v want ~128.48 Mpc", lin["distance_mpc"])
	}
	u := asMap(b, "units")
	for k, want := range map[string]string{"wavelength": "nm", "velocity": "km/s", "distance": "Mpc", "h0": "km/s/Mpc"} {
		if u[k] != want {
			t.Fatalf("unit %s=%v want %s", k, u[k], want)
		}
	}
}

// ---- batch + persistence ---------------------------------------------------

func TestAPIBatchAndPersistence(t *testing.T) {
	e := newTestEnv(t)
	body := map[string]any{
		"h0_km_s_mpc":  70,
		"relativistic": false,
		"lines": []map[string]any{
			{"line_id": "A", "rest_wavelength_nm": 500, "observed_wavelength_nm": 515}, // z=0.03
			{"line_id": "B", "rest_wavelength_nm": 100, "observed_wavelength_nm": 120}, // z=0.2 outside
			{"line_id": "C", "rest_wavelength_nm": 500, "observed_wavelength_nm": 490}, // blueshift
			{"line_id": "D", "redshift": 0.01},
			{"line_id": "E", "rest_wavelength_nm": 0, "observed_wavelength_nm": 500}, // invalid
		},
	}
	st, b := e.do("POST", "/api/v1/batch", body)
	if st != http.StatusOK {
		t.Fatalf("status=%d body=%v", st, b)
	}
	if int(num(b, "count")) != 5 {
		t.Fatalf("count=%v want 5", b["count"])
	}
	results, _ := b["results"].([]any)
	byID := map[string]map[string]any{}
	for _, ri := range results {
		rm := ri.(map[string]any)
		byID[rm["line_id"].(string)] = rm
	}

	a := byID["A"]
	if a["ok"] != true {
		t.Fatalf("A should succeed: %v", a)
	}
	ar := asMap(a, "result")
	if !closeFloats(num(ar, "redshift"), 0.03, 1e-12) {
		t.Fatalf("A z=%v", ar["redshift"])
	}
	if ar["linear_regime"] != true {
		t.Fatalf("A must be inside linear regime")
	}

	br := asMap(byID["B"], "result")
	if br["outside_linear"] != true || br["linear_regime"] != false {
		t.Fatalf("B must be flagged outside linear: %v", br)
	}

	cr := byID["C"]
	if cr["ok"] != false {
		t.Fatalf("C blueshift must be a structured per-line failure, not a success: %v", cr)
	}
	cerr := asMap(cr, "error")
	if cerr["code"] != "BLUESHIFT_DISTANCE_QUERY" || cerr["blueshift"] != true {
		t.Fatalf("C must be explicitly marked blueshift error: %v", cerr)
	}
	if _, present := cerr["distance_mpc"]; present {
		t.Fatalf("C blueshift must not carry distance")
	}
	if rz, ok := cerr["redshift"].(float64); !ok || rz >= 0 {
		t.Fatalf("C error must carry the negative redshift, got %v", cerr["redshift"])
	}

	dr := asMap(byID["D"], "result")
	if !closeFloats(num(dr, "redshift"), 0.01, 1e-12) {
		t.Fatalf("D z=%v want 0.01", dr["redshift"])
	}

	em := byID["E"]
	if em["ok"] != false || asMap(em, "error")["code"] != "NON_POSITIVE_REST_WAVELENGTH" {
		t.Fatalf("E must be a structured per-line error: %v", em)
	}

	// Every line (success or failure) must be persisted independently.
	items, err := e.st.Query(context.Background(), store.HistoryFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("persisted %d records, want 5", len(items))
	}

	// History filtering: only outside-linear records (B).
	st2, hb := e.do("GET", "/api/v1/history?outside_linear=true", nil)
	if st2 != http.StatusOK || int(num(hb, "count")) != 1 {
		t.Fatalf("outside_linear filter: %d %v", st2, hb)
	}
	// Only blueshift records (C).
	_, hb = e.do("GET", "/api/v1/history?blueshift=true", nil)
	if int(num(hb, "count")) != 1 {
		t.Fatalf("blueshift filter count=%v want 1", hb["count"])
	}
	// Only successful records (A, B, D; C is a blueshift failure, E invalid).
	_, hb = e.do("GET", "/api/v1/history?success=true", nil)
	if int(num(hb, "count")) != 3 {
		t.Fatalf("success filter count=%v want 3", hb["count"])
	}
	// Kind filter.
	_, hb = e.do("GET", "/api/v1/history?kind=distance", nil)
	if int(num(hb, "count")) != 5 {
		t.Fatalf("kind filter count=%v want 5", hb["count"])
	}
}

// request_id correlation: the id echoed on the response retrieves exactly the
// records created by that request.
func TestAPIHistoryByRequestID(t *testing.T) {
	e := newTestEnv(t)
	req, _ := http.NewRequest("POST", e.addr+"/api/v1/distance",
		bytes.NewReader([]byte(`{"redshift":0.03,"h0_km_s_mpc":70}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "corr-123")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	resp.Body.Close()
	if resp.Header.Get("X-Request-ID") != "corr-123" {
		t.Fatalf("X-Request-ID not echoed")
	}
	st, b := e.do("GET", "/api/v1/history?request_id=corr-123", nil)
	if st != http.StatusOK || int(num(b, "count")) != 1 {
		t.Fatalf("request_id filter: %d %v", st, b)
	}
}

// ---- concurrency -----------------------------------------------------------

// Many simultaneous requests with distinct inputs must never see each other's
// data, and every request must land exactly once in history.
func TestAPIConcurrentRequestsDoNotInterfere(t *testing.T) {
	e := newTestEnv(t)
	const n = 80
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			z := 0.01 + float64(i)*0.001 // unique z per goroutine
			body := map[string]any{"redshift": z, "h0_km_s_mpc": 70}
			req, _ := http.NewRequest("POST", e.addr+"/api/v1/distance",
				bytes.NewReader(mustJSON(body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Request-ID", fmt.Sprintf("g-%d", i))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs <- err
				return
			}
			defer resp.Body.Close()
			var out map[string]any
			raw, _ := io.ReadAll(resp.Body)
			_ = json.Unmarshal(raw, &out)
			if resp.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("goroutine %d status %d: %s", i, resp.StatusCode, raw)
				return
			}
			wantD := 299792.458 * z / 70
			if !closeFloats(num(out, "distance_mpc"), wantD, 1e-6) {
				errs <- fmt.Errorf("goroutine %d distance=%v want %v", i, out["distance_mpc"], wantD)
				return
			}
			if out["request_id"] != fmt.Sprintf("g-%d", i) {
				errs <- fmt.Errorf("goroutine %d got request_id %v", i, out["request_id"])
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	items, err := e.st.Query(context.Background(), store.HistoryFilter{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(items) != n {
		t.Fatalf("history has %d records, want %d (lost/duplicated under concurrency)", len(items), n)
	}
	seen := map[string]int{}
	for _, r := range items {
		seen[r.RequestID]++
	}
	for i := 0; i < n; i++ {
		if seen[fmt.Sprintf("g-%d", i)] != 1 {
			t.Fatalf("request g-%d persisted %d times, want exactly 1", i, seen[fmt.Sprintf("g-%d", i)])
		}
	}
}

// ---- health ----------------------------------------------------------------

func TestAPIHealth(t *testing.T) {
	e := newTestEnv(t)
	if st, b := e.do("GET", "/health/live", nil); st != http.StatusOK || b["status"] != "alive" {
		t.Fatalf("liveness: %d %v", st, b)
	}
	if st, b := e.do("GET", "/health/ready", nil); st != http.StatusOK || b["status"] != "ready" {
		t.Fatalf("readiness: %d %v", st, b)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
