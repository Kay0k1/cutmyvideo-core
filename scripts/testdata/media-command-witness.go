//go:build ignore

// Test-only native wrapper for counting real FFmpeg/FFprobe invocations.
// Build this file explicitly; it is never part of the application or archives.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(3)
}

func main() {
	path, err := os.Executable()
	if err != nil {
		fail(err)
	}
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		fail(err)
	}
	var config struct{ Tool, Log, Label string }
	if err := json.Unmarshal(data, &config); err != nil {
		fail(err)
	}
	if config.Tool == "" || config.Log == "" || config.Label == "" {
		fail(errors.New("incomplete media witness configuration"))
	}
	log, err := os.OpenFile(config.Log, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		fail(err)
	}
	_, writeErr := fmt.Fprintln(log, config.Label+" "+strings.Join(os.Args[1:], " "))
	closeErr := log.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		fail(err)
	}
	command := exec.Command(config.Tool, os.Args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		fail(err)
	}
}
