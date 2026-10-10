package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/app"
)

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr))
}

func run() error {
	return runArgs(os.Args[1:], os.Stdout, os.Stderr)
}

func runArgs(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprint(stdout, usage)
		return err
	}
	if args[0] == "version" || args[0] == "--version" {
		if len(args) != 1 {
			return invalidCLI(errors.New("version does not accept arguments"))
		}
		return printVersion(stdout)
	}
	if args[0] == "clip" {
		return runClip(args[1:], stdout, stderr)
	}
	if args[0] == "inspect" {
		return inspect(args[1:], stdout, stderr)
	}
	if args[0] != "server" && args[0] != "worker" && args[0] != "maintenance" && args[0] != "healthcheck" && args[0] != "worker-healthcheck" {
		return invalidCLI(fmt.Errorf("unknown command %q; use cutmy --help", args[0]))
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if flags.NArg() != 0 {
		return invalidCLI(fmt.Errorf("%s does not accept positional arguments", args[0]))
	}
	if args[0] == "worker-healthcheck" {
		return app.CheckWorkerHealth(os.Getenv("WORKER_HEALTH_PATH"))
	}
	if args[0] == "healthcheck" {
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
	c, err := app.ConfigFromEnv()
	if err != nil {
		return invalidCLI(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	openCtx, cancelOpen := context.WithTimeout(ctx, 30*time.Second)
	s, err := app.OpenStore(openCtx, c.DatabaseURL)
	cancelOpen()
	if err != nil {
		// Parser/connection errors can include the full DSN and its password.
		return &redactedCLIError{message: "could not initialize the database; check private configuration and database availability", cause: err}
	}
	defer s.DB.Close()
	switch args[0] {
	case "server":
		server := &http.Server{Addr: c.ListenAddr, Handler: app.NewServer(c, s).Handler(), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
		listener, listenErr := net.Listen("tcp", c.ListenAddr)
		if listenErr != nil {
			return listenErr
		}
		slog.Info("HTTP server started", "address", c.ListenAddr)
		return serveHTTP(ctx, server, listener, 10*time.Second)
	case "worker":
		return app.RunWorker(ctx, c, s)
	case "maintenance":
		return app.RunMaintenance(ctx, c, s)
	default:
		return errors.New("usage: cutmy server | worker | healthcheck | worker-healthcheck")
	}
}

// Shutdown closes the listener before it finishes draining requests. Wait for
// the drain before runArgs closes the store or main exits; force-close requests
// that exceed the shutdown budget.
func serveHTTP(ctx context.Context, server *http.Server, listener net.Listener, timeout time.Duration) error {
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() {
		<-serveCtx.Done()
		shutdownCtx, stop := context.WithTimeout(context.Background(), timeout)
		defer stop()
		err := server.Shutdown(shutdownCtx)
		if err != nil {
			_ = server.Close()
		}
		shutdownDone <- err
	}()
	err := server.Serve(listener)
	// A listener failure or an explicit Server.Close must join the same drain,
	// even when the caller has not cancelled its context.
	cancel()
	shutdownErr := <-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return shutdownErr
	}
	return err
}
