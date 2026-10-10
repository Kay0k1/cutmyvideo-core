package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestHTTPShutdownDrainsActiveRequestBeforeReturning(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = io.WriteString(w, "completed before database close")
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- serveHTTP(ctx, server, listener, 3*time.Second) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if err == nil {
			defer resp.Body.Close()
			var body []byte
			body, err = io.ReadAll(resp.Body)
			if err == nil && string(body) != "completed before database close" {
				err = errors.New("incomplete response")
			}
		}
		response <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-stopped:
		t.Fatalf("returned before request completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("response stuck")
	}
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown stuck")
	}
}

func TestHTTPShutdownDeadlineClosesStuckRequest(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	started, requestClosed := make(chan struct{}), make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		close(requestClosed)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- serveHTTP(ctx, server, listener, 50*time.Millisecond) }()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Get("http://" + listener.Addr().String())
		if resp != nil {
			resp.Body.Close()
		}
		response <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown exceeded its budget")
	}
	select {
	case <-requestClosed:
	case <-time.After(3 * time.Second):
		t.Fatal("request connection stayed open")
	}
	select {
	case err := <-response:
		if err == nil {
			t.Fatal("stuck request was not interrupted")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client stayed blocked")
	}
}
