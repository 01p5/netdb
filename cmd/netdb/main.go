package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"netdb/internal/config"
	"netdb/internal/httpx"
	"netdb/internal/kea"
	"netdb/internal/reconcile"
	"netdb/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config load", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		slog.Error("store open", "err", err, "path", cfg.DBPath)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.Migrate(context.Background()); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	rec := reconcile.New(st, reconcile.Config{
		TechnitiumURL:      cfg.TechnitiumURL,
		TechnitiumToken:    cfg.TechnitiumToken,
		TechnitiumUser:     cfg.TechnitiumUser,
		TechnitiumPassword: cfg.TechnitiumPassword,
		TechnitiumInsecure: cfg.TechnitiumInsecure,
		CloudflareToken:    cfg.CloudflareToken,
		CloudflareBaseURL:  cfg.CloudflareBaseURL,
	}, cfg.ReconcileInterval)

	keaSync := kea.New(st, cfg.KeaURL, cfg.KeaSyncInterval)
	leasePoller := kea.NewLeasePoller(st, cfg.KeaURL, cfg.KeaLeasePollEvery)

	appCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go rec.Start(appCtx)
	go keaSync.Start(appCtx)
	go leasePoller.Start(appCtx)

	srv := httpx.New(st, rec, keaSync, leasePoller, httpx.Options{
		AuthUser:     cfg.AuthUser,
		AuthPassword: cfg.AuthPassword,
	})
	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		slog.Info("listening", "addr", cfg.Addr, "db", cfg.DBPath,
			"reconcile_interval", cfg.ReconcileInterval)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("http serve", "err", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	slog.Info("shutting down")
	cancel()
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Error("shutdown", "err", err)
	}
}
