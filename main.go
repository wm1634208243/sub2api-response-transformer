package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	configPath := flag.String("config", envOrDefault("CONFIG_PATH", "/etc/transformer/config.json"), "path to JSON config")
	healthcheckURL := flag.String("healthcheck", "", "perform one HTTP health check and exit")
	flag.Parse()
	if *healthcheckURL != "" {
		if err := runHealthcheck(*healthcheckURL); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := loadConfig(*configPath)
	if err != nil {
		logger.Error("config_load_failed", "error", err)
		os.Exit(1)
	}

	store := &configStore{}
	store.Store(cfg)
	initialListen := cfg.Listen
	stats := &proxyStats{}
	proxy := newProxy(store, stats, logger)
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           newHandler(store, proxy, stats, *configPath, logger),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	stop := make(chan os.Signal, 2)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.Signal(1))

	go func() {
		for range reload {
			next, err := loadConfig(*configPath)
			if err != nil {
				logger.Error("config_reload_failed", "error", err)
				continue
			}
			if next.Listen != initialListen {
				logger.Warn("listen_change_requires_restart", "current", initialListen, "requested", next.Listen)
				next.Listen = initialListen
			}
			store.Store(next)
			logger.Info("config_reloaded", "upstream", next.Upstream, "rules", len(next.Rules))
		}
	}()

	go func() {
		logger.Info("proxy_started", "listen", cfg.Listen, "upstream", cfg.Upstream, "rules", len(cfg.Rules))
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server_failed", "error", err)
			os.Exit(1)
		}
	}()

	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("shutdown_failed", "error", err)
	}
}

func runHealthcheck(target string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		return fmt.Errorf("healthcheck request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("healthcheck returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
