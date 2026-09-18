// Package cosmology implements the pure physics calculations for the
// redshift / Hubble distance service.
//
// Fixed units (no mixed dimensions are allowed anywhere in the service):
//
//	wavelength  : nanometre (nm)
//	velocity    : kilometre per second (km/s)
//	distance    : megaparsec (Mpc)
//	Hubble const: kilometre per second per megaparsec (km/s/Mpc)
//
// Two distinct velocity<->redshift relations are supported and must never
// be conflated:
//
//   - cosmological linear relation (default): v = c*z, d = v/H0
//   - relativistic Doppler relation (opt-in switch):
//     v = c * ((1+z)^2 - 1) / ((1+z)^2 + 1)
package cosmology

import (
	"math"
)

// C is the speed of light in km/s, fixed for all calculations.
const C = 299792.458 // km/s

// LinearThreshold is the fixed redshift limit of the non-relativistic
// linear regime. For 0 <= z <= LinearThreshold the Hubble linear law is
// reliable; above it the same linear numbers may still be returned but the
// result must be flagged as outside the linear range.
const LinearThreshold = 0.1

// WarningOutsideLinear is attached to results beyond LinearThreshold.
const WarningOutsideLinear = "redshift exceeds the linear-regime threshold z=" +
	"0.1; the linear Hubble approximation is extrapolated and unreliable"

// Structured error codes. The HTTP layer maps these onto status codes.
const (
	ErrNonPositiveRestWavelength     = "NON_POSITIVE_REST_WAVELENGTH"
	ErrNonPositiveObservedWavelength = "NON_POSITIVE_OBSERVED_WAVELENGTH"
	ErrNonPositiveHubbleConstant     = "NON_POSITIVE_HUBBLE_CONSTANT"
	ErrMissingField                  = "MISSING_FIELD"
	ErrAmbiguousInput                = "AMBIGUOUS_INPUT"
	ErrBlueshiftDistance             = "BLUESHIFT_DISTANCE_QUERY"
	ErrInvalidNumber                 = "INVALID_NUMBER"
	ErrInvalidQueryParameter         = "INVALID_QUERY_PARAMETER"
)

// Error is the structured error produced by every validation/calculation
// failure. It is JSON-serializable and carries a stable machine-readable
// Code plus a human Message. Blueshift distance errors additionally carry
// the explicit Blueshift marker and the offending Redshift, so a blueshift
// is always answered AS a blueshift rather than masked.
type Error struct {
	Code      string   `json:"code"`
	Message   string   `json:"message"`
	Blueshift bool     `json:"blueshift,omitempty"`
	Redshift  *float64 `json:"redshift,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, msg string) *Error {
	return &Error{Code: code, Message: msg}
}

// BlueshiftDistanceError builds the structured error for a distance query on
// a blueshift. The explicit Blueshift flag and Redshift value are mandatory:
// the caller must never silently receive a negative "forward" distance.
func BlueshiftDistanceError(z float64) *Error {
	return &Error{
		Code: ErrBlueshiftDistance,
		Message: "blueshift detected (z<0): cosmological comoving distance is not defined; " +
			"no forward distance is returned",
		Blueshift: true,
		Redshift:  &z,
	}
}

// validNumber rejects non-positive, NaN and Inf floating point values.
func validNumber(x float64) bool {
	return x > 0 && !math.IsNaN(x) && !math.IsInf(x, 0)
}

// RedshiftFromWavelengths returns z = lambda_obs/lambda_rest - 1.
//
// The denominator is intentionally the REST wavelength: swapping the two
// wavelengths is a different (wrong) quantity. Positive result = redshift,
// negative = blueshift, zero = no shift.
func RedshiftFromWavelengths(lambdaRest, lambdaObs float64) (float64, *Error) {
	if !validNumber(lambdaRest) {
		return 0, newError(ErrNonPositiveRestWavelength,
			"rest wavelength must be a positive, finite number (nm)")
	}
	if !validNumber(lambdaObs) {
		return 0, newError(ErrNonPositiveObservedWavelength,
			"observed wavelength must be a positive, finite number (nm)")
	}
	return lambdaObs/lambdaRest - 1, nil
}

// LinearVelocity converts redshift to recession velocity with the default
// cosmological linear relation v = c*z. Works for negative z (blueshift).
func LinearVelocity(z float64) float64 { return C * z }

// RelativisticVelocity converts redshift to velocity with the special
// relativistic Doppler formula (opt-in switch only). Works for z > -1.
func RelativisticVelocity(z float64) float64 {
	r := 1 + z
	r2 := r * r
	return C * (r2 - 1) / (r2 + 1)
}

// VelocityFromRedshift dispatches on the explicit relation switch.
func VelocityFromRedshift(z float64, relativistic bool) float64 {
	if relativistic {
		return RelativisticVelocity(z)
	}
	return LinearVelocity(z)
}

// RedshiftFromVelocity is the inverse relativistic Doppler map, used when
// the caller supplies a velocity while the relativistic switch is on:
//
//	z = sqrt((1+beta)/(1-beta)) - 1
func RedshiftFromVelocity(v float64) (float64, *Error) {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, newError(ErrInvalidNumber, "velocity must be finite (km/s)")
	}
	beta := v / C
	if beta <= -1 || beta >= 1 {
		return 0, newError(ErrInvalidNumber,
			"relativistic velocity must satisfy |v| < c (299792.458 km/s)")
	}
	return math.Sqrt((1+beta)/(1-beta)) - 1, nil
}

// LinearDistance returns the low-redshift comoving distance d = v/H0 (Mpc).
func LinearDistance(v, h0 float64) (float64, *Error) {
	if !validNumber(h0) {
		return 0, newError(ErrNonPositiveHubbleConstant,
			"H0 must be a positive, finite number (km/s/Mpc)")
	}
	return v / h0, nil
}

// InLinearRegime reports whether z lies in the non-relativistic linear
// zone. It is defined on the non-negative domain: blueshifts (z < 0) are
// never distance queries, so they report false.
func InLinearRegime(z float64) bool { return z >= 0 && z <= LinearThreshold }

// Classification labels the shift direction.
func Classification(z float64) string {
	switch {
	case z > 0:
		return "redshift"
	case z < 0:
		return "blueshift"
	default:
		return "none"
	}
}
