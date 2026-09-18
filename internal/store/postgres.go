package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	_ "github.com/lib/pq"
)

// PostgresStore persists records in an external PostgreSQL database.
// database/sql manages its own connection pool and is safe for concurrent
// use, so concurrent HTTP requests cannot interleave rows.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore opens the connection pool and ensures the schema exists.
// The caller should pass a context carrying a startup deadline.
func NewPostgresStore(ctx context.Context, dsn string) (*PostgresStore, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	// Bound the pool so request concurrency cannot overwhelm Postgres.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	s := &PostgresStore{db: db}
	if err := s.waitReady(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// waitReady retries Ping until the database accepts connections or the
// context expires (compose starts the app before Postgres finishes boot).
func (s *PostgresStore) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var lastErr error
	for {
		err := s.db.PingContext(ctx)
		if err == nil {
			return nil
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return errors.Join(lastErr, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (s *PostgresStore) migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS redshift_records (
    id                   BIGSERIAL PRIMARY KEY,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    request_id           TEXT NOT NULL,
    kind                 TEXT NOT NULL,
    rest_wavelength_nm   DOUBLE PRECISION,
    observed_wavelength_nm DOUBLE PRECISION,
    redshift             DOUBLE PRECISION,
    velocity_km_s        DOUBLE PRECISION,
    distance_mpc         DOUBLE PRECISION,
    h0_km_s_mpc          DOUBLE PRECISION,
    relativistic         BOOLEAN NOT NULL DEFAULT FALSE,
    linear_regime        BOOLEAN NOT NULL DEFAULT FALSE,
    blueshift            BOOLEAN NOT NULL DEFAULT FALSE,
    outside_linear       BOOLEAN NOT NULL DEFAULT FALSE,
    success              BOOLEAN NOT NULL,
    error_code           TEXT NOT NULL DEFAULT '',
    error_message        TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_redshift_records_created_at ON redshift_records (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_redshift_records_request_id ON redshift_records (request_id);
CREATE INDEX IF NOT EXISTS idx_redshift_records_kind ON redshift_records (kind);
`)
	return err
}

// Save implements Store.
func (s *PostgresStore) Save(ctx context.Context, r *Record) error {
	created := r.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
INSERT INTO redshift_records (
    created_at, request_id, kind, rest_wavelength_nm, observed_wavelength_nm,
    redshift, velocity_km_s, distance_mpc, h0_km_s_mpc, relativistic,
    linear_regime, blueshift, outside_linear, success, error_code, error_message
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
RETURNING id`,
		created, r.RequestID, r.Kind,
		nullable(r.RestWavelength), nullable(r.ObservedWavelength),
		nullable(r.Redshift), nullable(r.Velocity), nullable(r.Distance),
		nullable(r.HubbleConstant), r.Relativistic, r.LinearRegime,
		r.Blueshift, r.OutsideLinear, r.Success, r.ErrorCode, r.ErrorMessage,
	)
	if err := row.Scan(&r.ID); err != nil {
		return err
	}
	r.CreatedAt = created
	return nil
}

// Query implements Store, newest first.
func (s *PostgresStore) Query(ctx context.Context, f HistoryFilter) ([]Record, error) {
	q := `SELECT id, created_at, request_id, kind, rest_wavelength_nm,
		observed_wavelength_nm, redshift, velocity_km_s, distance_mpc,
		h0_km_s_mpc, relativistic, linear_regime, blueshift, outside_linear,
		success, error_code, error_message
		FROM redshift_records WHERE TRUE`
	args := make([]any, 0, 10)
	n := 1
	if f.RequestID != "" {
		q += " AND request_id = $" + itoa(n)
		args = append(args, f.RequestID)
		n++
	}
	if f.Kind != "" {
		q += " AND kind = $" + itoa(n)
		args = append(args, f.Kind)
		n++
	}
	if f.Blueshift != nil {
		q += " AND blueshift = $" + itoa(n)
		args = append(args, *f.Blueshift)
		n++
	}
	if f.OutsideLinear != nil {
		q += " AND outside_linear = $" + itoa(n)
		args = append(args, *f.OutsideLinear)
		n++
	}
	if f.Success != nil {
		q += " AND success = $" + itoa(n)
		args = append(args, *f.Success)
		n++
	}
	if f.From != nil {
		q += " AND created_at >= $" + itoa(n)
		args = append(args, *f.From)
		n++
	}
	if f.To != nil {
		q += " AND created_at <= $" + itoa(n)
		args = append(args, *f.To)
		n++
	}
	q += " ORDER BY created_at DESC, id DESC"
	if f.Limit > 0 {
		q += " LIMIT $" + itoa(n)
		args = append(args, f.Limit)
		n++
	}
	if f.Offset > 0 {
		q += " OFFSET $" + itoa(n)
		args = append(args, f.Offset)
	}

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Record
	for rows.Next() {
		var (
			r                      Record
			rest, obs, z, v, d, h0 sql.NullFloat64
			code, msg              string
		)
		if err := rows.Scan(&r.ID, &r.CreatedAt, &r.RequestID, &r.Kind,
			&rest, &obs, &z, &v, &d, &h0, &r.Relativistic, &r.LinearRegime,
			&r.Blueshift, &r.OutsideLinear, &r.Success, &code, &msg); err != nil {
			return nil, err
		}
		r.RestWavelength = unnullable(rest)
		r.ObservedWavelength = unnullable(obs)
		r.Redshift = unnullable(z)
		r.Velocity = unnullable(v)
		r.Distance = unnullable(d)
		r.HubbleConstant = unnullable(h0)
		r.ErrorCode = code
		r.ErrorMessage = msg
		out = append(out, r)
	}
	return out, rows.Err()
}

// Ping implements Store.
func (s *PostgresStore) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// Close implements Store.
func (s *PostgresStore) Close() error { return s.db.Close() }

func nullable(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func unnullable(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [8]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
