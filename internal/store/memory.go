package store

import (
	"context"
	"sync"
	"time"
)

// MemoryStore is an in-process Store implementation. It exists so the
// service (and the whole test suite) can run without an external database.
// It is safe for concurrent use: every read and write holds the lock, and
// returned slices are copies, so goroutines can never observe or mutate
// each other's records.
type MemoryStore struct {
	mu      sync.Mutex
	nextID  int64
	records []Record
}

// NewMemoryStore constructs an empty MemoryStore.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

// Save implements Store.
func (m *MemoryStore) Save(_ context.Context, r *Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	saved := *r
	saved.ID = m.nextID
	if saved.CreatedAt.IsZero() {
		saved.CreatedAt = time.Now().UTC()
	}
	m.records = append(m.records, saved)
	r.ID = saved.ID
	r.CreatedAt = saved.CreatedAt
	return nil
}

// Query implements Store, newest first.
func (m *MemoryStore) Query(_ context.Context, f HistoryFilter) ([]Record, error) {
	m.mu.Lock()
	all := make([]Record, len(m.records))
	copy(all, m.records)
	m.mu.Unlock()

	out := make([]Record, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		r := all[i]
		if !matchFilter(r, f) {
			continue
		}
		out = append(out, cloneRecord(r))
	}
	if f.Offset > 0 {
		if f.Offset >= len(out) {
			out = nil
		} else {
			out = out[f.Offset:]
		}
	}
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

// Ping implements Store.
func (m *MemoryStore) Ping(_ context.Context) error { return nil }

// Close implements Store.
func (m *MemoryStore) Close() error { return nil }

func matchFilter(r Record, f HistoryFilter) bool {
	if f.RequestID != "" && r.RequestID != f.RequestID {
		return false
	}
	if f.Kind != "" && r.Kind != f.Kind {
		return false
	}
	if f.Blueshift != nil && r.Blueshift != *f.Blueshift {
		return false
	}
	if f.OutsideLinear != nil && r.OutsideLinear != *f.OutsideLinear {
		return false
	}
	if f.Success != nil && r.Success != *f.Success {
		return false
	}
	if f.From != nil && r.CreatedAt.Before(*f.From) {
		return false
	}
	if f.To != nil && r.CreatedAt.After(*f.To) {
		return false
	}
	return true
}

// cloneRecord copies the pointer fields too, so callers cannot mutate
// stored values through a query result.
func cloneRecord(r Record) Record {
	cp := r
	if r.RestWavelength != nil {
		v := *r.RestWavelength
		cp.RestWavelength = &v
	}
	if r.ObservedWavelength != nil {
		v := *r.ObservedWavelength
		cp.ObservedWavelength = &v
	}
	if r.Redshift != nil {
		v := *r.Redshift
		cp.Redshift = &v
	}
	if r.Velocity != nil {
		v := *r.Velocity
		cp.Velocity = &v
	}
	if r.Distance != nil {
		v := *r.Distance
		cp.Distance = &v
	}
	if r.HubbleConstant != nil {
		v := *r.HubbleConstant
		cp.HubbleConstant = &v
	}
	return cp
}
