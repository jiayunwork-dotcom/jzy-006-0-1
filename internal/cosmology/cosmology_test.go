package cosmology

import (
	"math"
	"testing"
)

func approx(got, want, tol float64) bool { return math.Abs(got-want) <= tol }

// Key invariant: doubling the OBSERVED wavelength while the rest wavelength
// is fixed must yield z' = 2z + 1, NOT 2z. This proves z = obs/rest - 1 is
// implemented with the correct definition and denominator.
func TestRedshiftDoublingObservedWavelength(t *testing.T) {
	rest := 500.0
	obs1 := 515.0
	z1, err := RedshiftFromWavelengths(rest, obs1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !approx(z1, 0.03, 1e-12) {
		t.Fatalf("z1 = %.10f, want 0.03", z1)
	}

	z2, err := RedshiftFromWavelengths(rest, 2*obs1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := 2*z1 + 1
	if !approx(z2, want, 1e-12) {
		t.Fatalf("after doubling observed wavelength: z2 = %.10f, want 2*z1+1 = %.10f", z2, want)
	}
	if approx(z2, 2*z1, 1e-12) {
		t.Fatalf("z2 must not equal 2*z1 (naive doubling): both %.10f", z2)
	}
}

// The rest wavelength is the denominator; swapping the two wavelengths is a
// different quantity and must not happen silently.
func TestRestWavelengthIsDenominator(t *testing.T) {
	z, _ := RedshiftFromWavelengths(500, 515)
	swapped, _ := RedshiftFromWavelengths(515, 500)
	if z <= 0 || swapped >= 0 {
		t.Fatalf("expected redshift vs swapped blueshift, got %v and %v", z, swapped)
	}
	if approx(z, -swapped, 1e-9) {
		t.Fatalf("z=obs/rest-1 must be asymmetric; got z=%v swapped=%v", z, swapped)
	}
}

func TestRedAndBlueClassification(t *testing.T) {
	z, _ := RedshiftFromWavelengths(100, 120)
	if z <= 0 || Classification(z) != "redshift" {
		t.Fatalf("longer observed wavelength must be a redshift, got %v", z)
	}
	z, _ = RedshiftFromWavelengths(100, 80)
	if z >= 0 || Classification(z) != "blueshift" {
		t.Fatalf("shorter observed wavelength must be a blueshift, got %v", z)
	}
	z, _ = RedshiftFromWavelengths(100, 100)
	if z != 0 || Classification(z) != "none" {
		t.Fatalf("equal wavelengths must give z=0, got %v", z)
	}
}

// z = 0 must give v = 0 and d = 0 under every relation.
func TestZeroRedshiftZeroVelocityZeroDistance(t *testing.T) {
	if v := LinearVelocity(0); v != 0 {
		t.Fatalf("linear v at z=0 = %v, want 0", v)
	}
	if v := RelativisticVelocity(0); v != 0 {
		t.Fatalf("relativistic v at z=0 = %v, want 0", v)
	}
	d, err := LinearDistance(0, 70)
	if err != nil || d != 0 {
		t.Fatalf("distance at v=0 = %v (err=%v), want 0", d, err)
	}
}

// At fixed redshift, doubling H0 must halve the comoving distance.
func TestDistanceInverselyProportionalToH0(t *testing.T) {
	z := 0.03
	v := LinearVelocity(z)
	d1, err := LinearDistance(v, 70)
	if err != nil {
		t.Fatalf("d1: %v", err)
	}
	d2, err := LinearDistance(v, 140)
	if err != nil {
		t.Fatalf("d2: %v", err)
	}
	if !approx(d2, d1/2, 1e-9) {
		t.Fatalf("doubling H0 must halve distance: d1=%.6f d2=%.6f want %.6f", d1, d2, d1/2)
	}
}

// Preset worked example (~3% redshift, H0 = 70 km/s/Mpc): results must match
// the hand calculation to the expected order of magnitude.
func TestPresetWorkedExample(t *testing.T) {
	z, err := RedshiftFromWavelengths(500, 515)
	if err != nil {
		t.Fatalf("z: %v", err)
	}
	v := LinearVelocity(z)
	d, err := LinearDistance(v, 70)
	if err != nil {
		t.Fatalf("d: %v", err)
	}
	if !approx(z, 0.03, 1e-12) {
		t.Fatalf("z = %.10f, want 0.03", z)
	}
	if !approx(v, 8993.77374, 1e-6) {
		t.Fatalf("v = %.6f km/s, want 8993.77374 km/s", v)
	}
	if !approx(d, 128.482482, 1e-4) {
		t.Fatalf("d = %.6f Mpc, want ~128.482482 Mpc", d)
	}
	// order-of-magnitude sanity: hundreds, not tens or thousands of Mpc
	if d < 100 || d > 200 {
		t.Fatalf("distance %.3f Mpc outside hand-calculated order of magnitude", d)
	}
}

func TestLinearRegimeThreshold(t *testing.T) {
	cases := []struct {
		z    float64
		want bool
	}{
		{0, true},
		{0.03, true},
		{LinearThreshold, true}, // boundary inclusive
		{0.1000001, false},
		{0.5, false},
		{2.0, false},
		{-0.01, false}, // blueshifts are never linear-zone distance queries
	}
	for _, c := range cases {
		if got := InLinearRegime(c.z); got != c.want {
			t.Errorf("InLinearRegime(%v) = %v, want %v", c.z, got, c.want)
		}
	}
}

// Default cosmological linear and opt-in relativistic Doppler relations must
// stay distinct: the switch actually changes the answer.
func TestRelationsAreNotConflated(t *testing.T) {
	z := 0.03
	vLin := VelocityFromRedshift(z, false)
	vRel := VelocityFromRedshift(z, true)
	if vLin == vRel {
		t.Fatalf("linear and relativistic velocities must differ at z=0.03")
	}
	wantRel := C * ((1+z)*(1+z) - 1) / ((1+z)*(1+z) + 1)
	if !approx(vRel, wantRel, 1e-9) {
		t.Fatalf("vRel = %.9f, want %.9f", vRel, wantRel)
	}
	if !approx(vLin, C*z, 1e-9) {
		t.Fatalf("vLin = %.9f, want %.9f", vLin, C*z)
	}
	// At tiny redshift the two must converge (linear is the low-z limit).
	if !approx(VelocityFromRedshift(1e-9, false), VelocityFromRedshift(1e-9, true), 1e-9) {
		t.Fatalf("relations should agree in the z->0 limit")
	}
}

// Relativistic inverse: velocity -> z must undo z -> velocity.
func TestRelativisticRoundTrip(t *testing.T) {
	for _, z := range []float64{-0.5, -0.03, 0, 0.03, 0.5, 2.0} {
		v := RelativisticVelocity(z)
		z2, err := RedshiftFromVelocity(v)
		if err != nil {
			t.Fatalf("z=%v: %v", z, err)
		}
		if !approx(z2, z, 1e-9) {
			t.Fatalf("round-trip z=%v -> v=%.6f -> z=%v", z, v, z2)
		}
	}
}

func TestNonPositiveInputsRejectedBeforeCalculation(t *testing.T) {
	if _, err := RedshiftFromWavelengths(0, 500); err == nil || err.Code != ErrNonPositiveRestWavelength {
		t.Fatalf("rest=0 must fail with %s", ErrNonPositiveRestWavelength)
	}
	if _, err := RedshiftFromWavelengths(-1, 500); err == nil || err.Code != ErrNonPositiveRestWavelength {
		t.Fatalf("rest=-1 must fail with %s", ErrNonPositiveRestWavelength)
	}
	if _, err := RedshiftFromWavelengths(500, 0); err == nil || err.Code != ErrNonPositiveObservedWavelength {
		t.Fatalf("obs=0 must fail with %s", ErrNonPositiveObservedWavelength)
	}
	if _, err := RedshiftFromWavelengths(500, -2); err == nil || err.Code != ErrNonPositiveObservedWavelength {
		t.Fatalf("obs=-2 must fail with %s", ErrNonPositiveObservedWavelength)
	}
	if _, err := LinearDistance(1000, 0); err == nil || err.Code != ErrNonPositiveHubbleConstant {
		t.Fatalf("H0=0 must fail with %s", ErrNonPositiveHubbleConstant)
	}
	if _, err := LinearDistance(1000, -70); err == nil || err.Code != ErrNonPositiveHubbleConstant {
		t.Fatalf("H0=-70 must fail with %s", ErrNonPositiveHubbleConstant)
	}
}

func TestRelativisticVelocityBounds(t *testing.T) {
	if _, err := RedshiftFromVelocity(C); err == nil || err.Code != ErrInvalidNumber {
		t.Fatalf("v=c must be rejected")
	}
	if _, err := RedshiftFromVelocity(-C); err == nil || err.Code != ErrInvalidNumber {
		t.Fatalf("v=-c must be rejected")
	}
}
