// Command sqlp_serv is a small web UI for the sqlp library: paste SQL, pick
// a dialect and an action, and see the query structure, the columns used or
// the syntax tree. With -db, every run is recorded in a SQLite database.
//
//	go run -C cmd/sqlp_serv . -addr 127.0.0.1:9393 -db /tmp/sqlp_runs.db
//
// It is a module of its own, so that the SQLite driver does not become a
// dependency of the sqlp library. Deployment notes are in deploy/README.md.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:9393", "address to listen on")
	dbPath := flag.String("db", "", "SQLite database to record every run in; none when empty")
	dbMaxMB := flag.Int64("db-max-mb", 1024, "with -db: the most the database may grow to, in MB; the oldest runs are deleted to stay below it")
	flag.Parse()
	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := serve(*addr, *dbPath, *dbMaxMB<<20, log); err != nil {
		log.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func serve(addr, dbPath string, dbMaxBytes int64, log *slog.Logger) error {
	s, err := newServer(log)
	if err != nil {
		return err
	}
	if dbPath != "" {
		if s.store, err = openStore(dbPath, dbMaxBytes, log); err != nil {
			return err
		}
		// Runs are written until the last request is done.
		defer func() {
			if err := s.store.close(); err != nil {
				log.Error("closing the store", "err", err)
			}
		}()
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", addr, "build", s.build, "db", dbPath)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Info("shutting down")
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
