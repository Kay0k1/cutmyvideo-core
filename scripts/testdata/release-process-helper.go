//go:build ignore

// Test-only executable for archive cancellation acceptance. Build this file
// natively, never as a replacement for the archived cutmy application.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"
)

type tree struct {
	Addresses []string `json:"addresses"`
	PIDs      []int    `json:"pids"`
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(3)
}

func main() {
	level := 0
	if len(os.Args) == 3 && os.Args[1] == "--distribution-child" {
		var err error
		level, err = strconv.Atoi(os.Args[2])
		if err != nil || level < 1 || level > 2 {
			fail(fmt.Errorf("invalid helper level"))
		}
	}
	marker := os.Getenv("CUTMY_DIST_PROCESS_MARKER")
	if marker == "" {
		fail(fmt.Errorf("helper marker is required"))
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fail(err)
	}
	defer listener.Close()
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = io.WriteString(connection, "cutmy-distribution-helper-v1")
			_ = connection.Close()
		}
	}()
	result := tree{[]string{listener.Addr().String()}, []int{os.Getpid()}}
	// A failure before the complete tree marker must still leave enough owned
	// identity for the driver to rescue this particular helper after failing.
	identity, err := json.Marshal(result)
	if err != nil {
		fail(err)
	}
	if err := os.WriteFile(marker+".pid."+strconv.Itoa(level), identity, 0600); err != nil {
		fail(err)
	}
	var child *exec.Cmd
	if level < 2 {
		executable, err := os.Executable()
		if err != nil {
			fail(err)
		}
		child = exec.Command(executable, "--distribution-child", strconv.Itoa(level+1))
		child.Stdout, child.Stderr = os.Stdout, os.Stderr
		if err := child.Start(); err != nil {
			fail(err)
		}
		until := time.Now().Add(3 * time.Second)
		var descendants tree
		for {
			data, err := os.ReadFile(marker + "." + strconv.Itoa(level+1))
			if err == nil && json.Unmarshal(data, &descendants) == nil {
				break
			}
			if time.Now().After(until) {
				fail(fmt.Errorf("child marker was not ready"))
			}
			time.Sleep(10 * time.Millisecond)
		}
		result.Addresses = append(result.Addresses, descendants.Addresses...)
		result.PIDs = append(result.PIDs, descendants.PIDs...)
	}
	data, err := json.Marshal(result)
	if err != nil {
		fail(err)
	}
	path := marker
	if level != 0 {
		path += "." + strconv.Itoa(level)
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		fail(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		fail(err)
	}
	if child != nil {
		fail(child.Wait())
	}
	select {}
}
