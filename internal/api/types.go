// Package api exposes the redshift/Hubble-distance calculation service over
// HTTP. Every calculation is validated, executed through internal/cosmology
// and persisted through internal/store.
package api

import (
	"math"

	"cosmoredshift/internal/cosmology"
	"cosmoredshift/internal/store"
)

// ---- request DTOs ----------------------------------------------------------

// RedshiftRequest accepts exactly one input mode:
//   - rest_wavelength_nm + observed_wavelength_nm
//   - redshift
//   - velocity_km_s (inverted via z=v/c by default, or via the inverse
//     relativistic Doppler formula when relativistic=true)
type RedshiftRequest struct {
	RestWavelengthNm     *float64 `json:"rest_wavelength_nm"`
	ObservedWavelengthNm *float64 `json:"observed_wavelength_nm"`
	Redshift             *float64 `json:"redshift"`
	VelocityKmS          *float64 `json:"velocity_km_s"`
	Relativistic         bool     `json:"relativistic"`
}

// DistanceRequest adds the required H0 to the redshift inputs.
type DistanceRequest struct {
	RedshiftRequest
	H0KmSMpc *float64 `json:"h0_km_s_mpc"`
}

// BatchLine is one spectrum line in a batch request.
type BatchLine struct {
	LineID               string   `json:"line_id"`
	RestWavelengthNm     *float64 `json:"rest_wavelength_nm"`
	ObservedWavelengthNm *float64 `json:"observed_wavelength_nm"`
	Redshift             *float64 `json:"redshift"`
	VelocityKmS          *float64 `json:"velocity_km_s"`
	H0KmSMpc             *float64 `json:"h0_km_s_mpc"`
}

// BatchRequest submits many spectrum lines. Top-level H0 is the default for
// lines that do not supply their own; the relativistic switch is global.
type BatchRequest struct {
	H0KmSMpc     *float64    `json:"h0_km_s_mpc"`
	Relativistic bool        `json:"relativistic"`
	Lines        []BatchLine `json:"lines"`
}

// ---- response DTOs ---------------------------------------------------------

// RedshiftResponse is returned by POST /redshift.
type RedshiftResponse struct {
	RequestID      string  `json:"request_id"`
	Redshift       float64 `json:"redshift"`
	Classification string  `json:"classification"`
	Blueshift      bool    `json:"blueshift"`
	VelocityKmS    float64 `json:"velocity_km_s"`
	Relation       string  `json:"relation"`
	Relativistic   bool    `json:"relativistic"`
	Units          Units   `json:"units"`
}

// DistanceResponse is returned by POST /distance.
type DistanceResponse struct {
	RequestID      string   `json:"request_id"`
	Redshift       float64  `json:"redshift"`
	Classification string   `json:"classification"`
	Blueshift      bool     `json:"blueshift"`
	VelocityKmS    float64  `json:"velocity_km_s"`
	DistanceMpc    *float64 `json:"distance_mpc,omitempty"`
	H0KmSMpc       *float64 `json:"h0_km_s_mpc,omitempty"`
	Relation       string   `json:"relation"`
	Relativistic   bool     `json:"relativistic"`
	LinearRegime   bool     `json:"linear_regime"`
	OutsideLinear  bool     `json:"outside_linear"`
	Warning        string   `json:"warning,omitempty"`
	Units          Units    `json:"units"`
}

// Units documents the fixed dimensions of every numeric field.
type Units struct {
	Wavelength string `json:"wavelength"`
	Velocity   string `json:"velocity"`
	Distance   string `json:"distance"`
	H0         string `json:"h0"`
}

var fixedUnits = Units{
	Wavelength: "nm",
	Velocity:   "km/s",
	Distance:   "Mpc",
	H0:         "km/s/Mpc",
}

// BatchLineResult is one line's outcome inside a batch response.
type BatchLineResult struct {
	LineID string            `json:"line_id"`
	OK     bool              `json:"ok"`
	Result *DistanceResponse `json:"result,omitempty"`
	Error  *cosmology.Error  `json:"error,omitempty"`
}

// BatchResponse is returned by POST /batch.
type BatchResponse struct {
	RequestID string            `json:"request_id"`
	Count     int               `json:"count"`
	Results   []BatchLineResult `json:"results"`
	Units     Units             `json:"units"`
}

// HistoryResponse is returned by GET /history.
type HistoryResponse struct {
	Count int            `json:"count"`
	Items []store.Record `json:"items"`
}

// apiError is the single structured error envelope for every failure.
type apiError struct {
	Error cosmology.Error `json:"error"`
}

// ---- shared resolution -----------------------------------------------------

// resolveInput derives a redshift from the three supported input modes.
// Exactly one mode must be supplied. Blueshifts (z<0) resolve successfully
// here; it is the distance step that rejects them.
func resolveInput(rr RedshiftRequest) (float64, *cosmology.Error) {
	hasWave := rr.RestWavelengthNm != nil || rr.ObservedWavelengthNm != nil
	if hasWave {
		if rr.RestWavelengthNm == nil {
			return 0, &cosmology.Error{Code: cosmology.ErrMissingField,
				Message: "rest_wavelength_nm is required together with observed_wavelength_nm"}
		}
		if rr.ObservedWavelengthNm == nil {
			return 0, &cosmology.Error{Code: cosmology.ErrMissingField,
				Message: "observed_wavelength_nm is required together with rest_wavelength_nm"}
		}
		if rr.Redshift != nil || rr.VelocityKmS != nil {
			return 0, &cosmology.Error{Code: cosmology.ErrAmbiguousInput,
				Message: "provide wavelengths OR redshift/velocity, not both"}
		}
		z, err := cosmology.RedshiftFromWavelengths(*rr.RestWavelengthNm, *rr.ObservedWavelengthNm)
		if err != nil {
			return 0, err
		}
		return z, nil
	}
	if rr.Redshift != nil {
		if rr.VelocityKmS != nil {
			return 0, &cosmology.Error{Code: cosmology.ErrAmbiguousInput,
				Message: "provide redshift OR velocity_km_s, not both"}
		}
		return *rr.Redshift, nil
	}
	if rr.VelocityKmS != nil {
		v := *rr.VelocityKmS
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, &cosmology.Error{Code: cosmology.ErrInvalidNumber,
				Message: "velocity_km_s must be a finite number"}
		}
		if rr.Relativistic {
			// Opt-in relativistic inverse Doppler:
			// z = sqrt((1+beta)/(1-beta)) - 1, |v| < c.
			z, err := cosmology.RedshiftFromVelocity(v)
			if err != nil {
				return 0, err
			}
			return z, nil
		}
		// Default cosmological linear relation is invertible as z = v/c.
		return v / cosmology.C, nil
	}
	return 0, &cosmology.Error{Code: cosmology.ErrMissingField,
		Message: "provide rest_wavelength_nm+observed_wavelength_nm, or redshift, or velocity_km_s"}
}
