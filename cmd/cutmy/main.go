package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
)

func main() {
	if err := run(); err != nil {
		slog.Error("cutmy stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: cutmy server | worker | clip | healthcheck | worker-healthcheck")
	}
	if os.Args[1] == "clip" {
		return clip(os.Args[2:])
	}
	if os.Args[1] == "worker-healthcheck" {
		return app.CheckWorkerHealth(os.Getenv("WORKER_HEALTH_PATH"))
	}
	if os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/readyz")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return fmt.Errorf("not ready: %d", resp.StatusCode)
		}
		return nil
	}
	flag.CommandLine = flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	if err := flag.CommandLine.Parse(os.Args[2:]); err != nil {
		return err
	}
	c, err := app.ConfigFromEnv()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	s, err := app.OpenStore(ctx, c.DatabaseURL)
	if err != nil {
		return err
	}
	defer s.DB.Close()
	switch os.Args[1] {
	case "server":
		server := &http.Server{Addr: c.ListenAddr, Handler: app.NewServer(c, s).Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		}()
		slog.Info("HTTP server started", "address", c.ListenAddr)
		err = server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case "worker":
		return app.RunWorker(ctx, c, s)
	default:
		return errors.New("usage: cutmy server | worker | healthcheck | worker-healthcheck")
	}
}
