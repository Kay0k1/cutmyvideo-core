package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestProcessCancellationTerminatesDescendants(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "child-address")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := runCommand(ctx, executable, "-test.run=^TestProcessTreeFixture$", "--", "tree-parent", marker)
		result <- err
	}()
	var address string
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			address = string(data)
			break
		}
		select {
		case err := <-result:
			t.Fatalf("parent stopped before spawning descendant: %v", err)
		default:
		}
		time.Sleep(10 * time.Millisecond)
	}
	if address == "" {
		t.Fatal("descendant did not become ready")
	}
	connection, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("descendant was not running: %v", err)
	}
	_ = connection.Close()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation cause lost: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("process tree cancellation hung")
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = connection.Close()
		if time.Now().After(deadline) {
			t.Fatal("descendant survived cancellation")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A child listener makes liveness observable without PID reuse or platform
// differences in zombie-process reporting. The grandchild starts immediately.
func TestProcessTreeFixture(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	mode, marker := os.Args[len(os.Args)-2], os.Args[len(os.Args)-1]
	if mode == "tree-parent" || mode == "tree-child" {
		executable, err := os.Executable()
		if err != nil {
			os.Exit(3)
		}
		next := "tree-child"
		if mode == "tree-child" {
			next = "tree-leaf"
		}
		cmd := exec.Command(executable, "-test.run=^TestProcessTreeFixture$", "--", next, marker)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if cmd.Start() != nil {
			os.Exit(3)
		}
		if cmd.Wait() != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	if mode != "tree-leaf" {
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	if os.WriteFile(marker, []byte(listener.Addr().String()), 0600) != nil {
		os.Exit(3)
	}
	for {
		connection, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		_, _ = io.WriteString(connection, "alive")
		_ = connection.Close()
	}
}
