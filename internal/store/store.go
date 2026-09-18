// Package store persists redshift/distance accounting requests and results.
package store

import (
	"context"
	"time"
)

// Kind identifies the calculation endpoint a record belongs to.
const (
	KindRedshift = "redshift"
	KindDistance = "distance"
)

// Record is one persisted accounting request together with its outcome.
// Successful and rejected requests are both stored; failed attempts carry
// an ErrorCode/ErrorMessage instead of numeric results.
type Record struct {
	ID                 int64     `json:"id"`
	CreatedAt          time.Time `json:"created_at"`
	RequestID          string    `json:"request_id"`
	Kind               string    `json:"kind"`
	RestWavelength     *float64  `json:"rest_wavelength_nm,omitempty"`
	ObservedWavelength *float64  `json:"observed_wavelength_nm,omitempty"`
	Redshift           *float64  `json:"redshift,omitempty"`
	Velocity           *float64  `json:"velocity_km_s,omitempty"`
	Distance           *float64  `json:"distance_mpc,omitempty"`
	HubbleConstant     *float64  `json:"h0_km_s_mpc,omitempty"`
	Relativistic       bool      `json:"relativistic"`
	LinearRegime       bool      `json:"linear_regime"`
	Blueshift          bool      `json:"blueshift"`
	OutsideLinear      bool      `json:"outside_linear"`
	Success            bool      `json:"success"`
	ErrorCode          string    `json:"error_code,omitempty"`
	ErrorMessage       string    `json:"error_message,omitempty"`
}

// HistoryFilter narrows history queries. Zero values mean "no constraint".
type HistoryFilter struct {
	RequestID     string
	Kind          string
	Blueshift     *bool
	OutsideLinear *bool
	Success       *bool
	From          *time.Time
	To            *time.Time
	Limit         int
	Offset        int
}

// Store is the persistence interface. Implementations MUST be safe for
// concurrent use.
type Store interface {
	// Save inserts one record and populates Record.ID/CreatedAt.
	Save(ctx context.Context, r *Record) error
	// Query returns matching records newest-first.
	Query(ctx context.Context, f HistoryFilter) ([]Record, error)
	// Ping is used by readiness checks.
	Ping(ctx context.Context) error
	// Close releases resources.
	Close() error
}
