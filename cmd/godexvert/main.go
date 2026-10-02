package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/jsixface/godexvert/internal/config"
	"github.com/jsixface/godexvert/internal/convert"
	"github.com/jsixface/godexvert/internal/events"
	"github.com/jsixface/godexvert/internal/library"
	"github.com/jsixface/godexvert/internal/media"
	"github.com/jsixface/godexvert/internal/store"
	"github.com/jsixface/godexvert/internal/watcher"
	"github.com/jsixface/godexvert/internal/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	var level slog.Level
	level.UnmarshalText([]byte(env("LOG_LEVEL", "info")))
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfgPath := env("CONFIG", config.DefaultPath())
	var fallbacks []string
	if os.Getenv("CONFIG") == "" {
		fallbacks = append(fallbacks, config.LegacyPath()) // carry over CodeXvert settings
	}
	cfg, err := config.Load(cfgPath, fallbacks...)
	if err != nil {
		return err
	}
	log.Info("settings", "file", cfgPath)

	dbPath := env("DB_PATH", filepath.Join("data", "godexvert.db"))
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Info("database", "path", dbPath)

	lib := library.New(st, cfg, library.FFProbe, log.With("c", "library"))
	if err := lib.Load(ctx); err != nil {
		return err
	}
	queue := convert.NewQueue(st, cfg, convert.FFmpeg, log.With("c", "convert"))
	bus := events.NewBroker()
	w := watcher.New(cfg, lib,
		func(f media.File, s media.Specs) { queue.Enqueue(f, s) }, queue.Has, log.With("c", "watcher"))

	srv, err := web.New(ctx, web.Deps{Log: log, Cfg: cfg, Store: st, Lib: lib, Queue: queue, Bus: bus, Watcher: w})
	if err != nil {
		return err
	}
	queue.OnUpdate = srv.JobUpdated
	queue.OnChange = srv.JobsChanged
	queue.OnDone = func(path string) {
		if err := lib.Refresh(ctx, path); err != nil {
			log.Warn("refresh converted file", "path", path, "err", err)
		}
		srv.LibraryChanged()
	}

	var wg sync.WaitGroup
	wg.Go(func() { queue.Run(ctx) })
	if err := w.Start(ctx); err != nil {
		log.Error("watcher", "err", err)
	}
	defer w.Stop()
	// The database only caches the library; a scan at startup catches changes made while down.
	srv.Rescan()

	addr := env("BIND", "localhost") + ":" + env("PORT", "8080")
	httpSrv := &http.Server{Addr: addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		log.Info("shutting down")
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()

	log.Info("listening", "addr", "http://"+strings.TrimPrefix(addr, ":"))
	if err := httpSrv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	wg.Wait() // let a running job record its cancellation
	return nil
}
