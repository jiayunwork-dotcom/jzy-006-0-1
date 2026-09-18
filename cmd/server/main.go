// Command server runs the redshift/Hubble-distance accounting service.
//
// Storage backend is selected with STORE_BACKEND:
//
//	STORE_BACKEND=postgres (default in container/compose)
//	    Uses the external PostgreSQL given by DATABASE_URL / PG* variables.
//	STORE_BACKEND=memory
//	    In-process store, for zero-dependency local runs and tests.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"cosmoredshift/internal/api"
	"cosmoredshift/internal/store"
)

func main() {
	logger := log.New(os.Stdout, "cosmo ", log.LstdFlags|log.Lmsgprefix)

	backend := getenv("STORE_BACKEND", "postgres")
	var st store.Store
	switch backend {
	case "memory":
		logger.Printf("using in-memory store")
		st = store.NewMemoryStore()
	case "postgres":
		dsn := getenv("DATABASE_URL", "")
		if dsn == "" {
			dsn = "host=" + getenv("PGHOST", "db") +
				" port=" + getenv("PGPORT", "5432") +
				" user=" + getenv("PGUSER", "cosmo") +
				" password=" + getenv("PGPASSWORD", "cosmo") +
				" dbname=" + getenv("PGDATABASE", "cosmo") +
				" sslmode=disable"
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		ps, err := store.NewPostgresStore(ctx, dsn)
		cancel()
		if err != nil {
			logger.Fatalf("postgres unavailable: %v", err)
		}
		st = ps
		logger.Printf("connected to postgres")
	default:
		logger.Fatalf("unknown STORE_BACKEND %q (want postgres or memory)", backend)
	}
	defer func() { _ = st.Close() }()

	srv := &http.Server{
		Addr:              ":" + getenv("PORT", "8080"),
		Handler:           api.NewServer(st, logger).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		logger.Printf("listening on %s", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Fatalf("http server: %v", err)
		}
	}()

	<-stop
	logger.Printf("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.Printf("graceful shutdown failed: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
