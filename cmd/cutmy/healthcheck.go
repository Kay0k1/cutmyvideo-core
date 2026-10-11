package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"
)

func healthcheckAddress(address string) (string, error) {
	if address == "" {
		address = ":8080"
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", invalidCLI(fmt.Errorf("LISTEN_ADDR must contain a host and port"))
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return "", invalidCLI(fmt.Errorf("LISTEN_ADDR port must be between 1 and 65535"))
	}
	if host == "" || host == "0.0.0.0" {
		host = "127.0.0.1"
	} else if host == "::" {
		host = "::1"
	}
	return net.JoinHostPort(host, port), nil
}

func checkAPIHealth(ctx context.Context, address string) error {
	address, err := healthcheckAddress(address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/readyz", nil)
	if err != nil {
		return invalidCLI(err)
	}
	// A local readiness probe must not leave the host through HTTP_PROXY.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %d", resp.StatusCode)
	}
	return nil
}
