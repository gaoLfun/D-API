package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gaoLfun/dapi/internal/alerts"
	"github.com/gaoLfun/dapi/internal/app"
	"github.com/gaoLfun/dapi/internal/auth"
	"github.com/gaoLfun/dapi/internal/config"
	"github.com/gaoLfun/dapi/internal/cryptox"
	"github.com/gaoLfun/dapi/internal/gateway"
	"github.com/gaoLfun/dapi/internal/httpapi"
	"github.com/gaoLfun/dapi/internal/ops"
	"github.com/gaoLfun/dapi/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("dapi stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	box, err := cryptox.NewSecretBox(cfg.MasterKey)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	db, err := store.Open(ctx, cfg.DatabaseURL, box)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "reset-password" {
		return resetPassword(ctx, db, cfg.AdminUsername, cfg.AdminPassword)
	}
	if err := httpapi.BootstrapAdmin(ctx, db, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return err
	}

	mux := http.NewServeMux()
	prober := ops.NewProber(nil, 10*time.Second)
	operations := app.Operations{Store: db, Prober: prober}
	notifier := app.ChannelNotifier{Store: db}
	httpapi.New(db, cfg, operations, notifier).Register(mux)
	mux.Handle("/v1/", gateway.NewHandler(app.GatewayRepository{Store: db}))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		status := http.StatusOK
		payload := map[string]string{"status": "ok"}
		checkCtx, checkCancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer checkCancel()
		if err := db.Ready(checkCtx); err != nil {
			status = http.StatusServiceUnavailable
			payload = map[string]string{"status": "unavailable"}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	})
	mux.Handle("/", httpapi.Static(cfg.WebDir))
	monitor := app.NewMonitor(db, prober, cfg.HealthEvery, cfg.BalanceEvery)
	go func() {
		if err := monitor.Run(ctx); err != nil && ctx.Err() == nil {
			slog.Error("operations monitor stopped", "error", err)
		}
	}()
	alertEngine := alerts.NewEngine(app.AlertRepository{Store: db}, notifier)
	go func() {
		if err := alertEngine.Run(ctx, time.Minute); err != nil && ctx.Err() == nil {
			slog.Error("alert engine stopped", "error", err)
		}
	}()
	go cleanup(ctx, db, cfg.LogRetention)

	server := &http.Server{
		Addr:              cfg.Addr,
		Handler:           httpapi.ProxyHeaders(mux, cfg.TrustProxy),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	errCh := make(chan error, 1)
	go func() {
		slog.Info("dapi listening", "config", cfg.String())
		errCh <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer shutdownCancel()
		return server.Shutdown(shutdownCtx)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func resetPassword(ctx context.Context, database *store.Store, username, password string) error {
	if username == "" || password == "" {
		return errors.New("reset-password requires DAPI_ADMIN_USERNAME and DAPI_ADMIN_PASSWORD")
	}
	admin, err := database.AdminByUsername(ctx, username)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := database.UpdateAdminPassword(ctx, admin.ID, hash); err != nil {
		return err
	}
	adminID := admin.ID
	if err := database.WriteAudit(ctx, &adminID, "admin.password_reset", "admin", username, nil, "cli"); err != nil {
		slog.Error("password reset audit write failed", "error", err)
	}
	slog.Info("administrator password reset", "username", username)
	return nil
}

func cleanup(ctx context.Context, database *store.Store, retention time.Duration) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()
	for {
		if err := database.CleanupLogs(ctx, time.Now().Add(-retention)); err != nil && ctx.Err() == nil {
			slog.Error("request log cleanup failed", "error", err)
		}
		if err := database.DeleteExpiredSessions(ctx); err != nil && ctx.Err() == nil {
			slog.Error("expired session cleanup failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
