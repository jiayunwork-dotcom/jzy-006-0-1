package store

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

func TestMemoryStoreSaveAndQueryNewestFirst(t *testing.T) {
	st := NewMemoryStore()
	ctx := context.Background()

	z1, z2 := 0.01, 0.02
	r1 := &Record{RequestID: "r1", Kind: KindDistance, Redshift: &z1, Success: true}
	r2 := &Record{RequestID: "r2", Kind: KindRedshift, Redshift: &z2, Success: true}
	if err := st.Save(ctx, r1); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := st.Save(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if r1.ID == 0 || r2.ID != 2 {
		t.Fatalf("ids not assigned: %d %d", r1.ID, r2.ID)
	}

	got, err := st.Query(ctx, HistoryFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].RequestID != "r2" {
		t.Fatalf("want newest-first [r2 r1], got %v", got)
	}

	got, _ = st.Query(ctx, HistoryFilter{Kind: KindRedshift})
	if len(got) != 1 || got[0].RequestID != "r2" {
		t.Fatalf("kind filter failed: %v", got)
	}
}

func TestMemoryStoreConcurrentSavesDoNotCorrupt(t *testing.T) {
	st := NewMemoryStore()
	ctx := context.Background()
	const n = 200
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			z := float64(i)
			_ = st.Save(ctx, &Record{RequestID: fmt.Sprintf("c-%d", i), Redshift: &z})
		}(i)
	}
	wg.Wait()
	got, err := st.Query(ctx, HistoryFilter{Limit: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("got %d records, want %d", len(got), n)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.RequestID] {
			t.Fatalf("duplicate record for %s", r.RequestID)
		}
		seen[r.RequestID] = true
	}
}

// TestPostgresStore runs against a real external database only when
// TEST_DATABASE_URL is set (the compose stack provides one).
func TestPostgresStore(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping postgres integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer st.Close()

	z := 0.03
	rec := &Record{
		RequestID: "pg-test", Kind: KindDistance, Redshift: &z, Success: true,
		LinearRegime: true,
	}
	if err := st.Save(ctx, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	if rec.ID == 0 {
		t.Fatalf("id not assigned")
	}
	got, err := st.Query(ctx, HistoryFilter{RequestID: "pg-test"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 1 || got[0].Redshift == nil || *got[0].Redshift != z {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := st.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
}
