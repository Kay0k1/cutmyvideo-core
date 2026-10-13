//go:build ignore

// Owned offline measurement wrapper. Arguments are passed through but never logged.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	data, err := os.ReadFile(path + ".json")
	if err != nil {
		panic(err)
	}
	var c struct {
		Tool, Label, Log string
		LocalInputs      []string
	}
	if err := json.Unmarshal(data, &c); err != nil {
		panic(err)
	}
	if c.Tool == "" || c.Log == "" || (c.Label != "ffmpeg" && c.Label != "ffprobe") {
		panic("incomplete owned tool configuration")
	}
	local := false
	var threads []string
	for index, value := range os.Args[1:] {
		for _, input := range c.LocalInputs {
			if value == input {
				local = true
			}
		}
		if value == "-threads" && index+2 < len(os.Args) {
			threads = append(threads, os.Args[index+2])
		}
	}
	command := exec.Command(c.Tool, os.Args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	start := time.Now().UnixNano()
	err = command.Run()
	end := time.Now().UnixNano()
	exit := 0
	if err != nil {
		exit = 3
		var status *exec.ExitError
		if errors.As(err, &status) {
			exit = status.ExitCode()
		}
	}
	pid := 0
	if command.Process != nil {
		pid = command.Process.Pid
	}
	entry, _ := json.Marshal(map[string]any{"tool": c.Label, "start_ns": start, "end_ns": end, "pid": pid, "exit": exit, "local_input": local, "explicit_threads": threads})
	log, logErr := os.OpenFile(c.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if logErr == nil {
		_, logErr = log.Write(append(entry, '\n'))
		closeErr := log.Close()
		if logErr == nil {
			logErr = closeErr
		}
	}
	if logErr != nil {
		fmt.Fprintln(os.Stderr, "owned tool receipt failed:", logErr)
		os.Exit(3)
	}
	os.Exit(exit)
}
