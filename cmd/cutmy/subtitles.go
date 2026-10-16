package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func runSubtitles(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy subtitles retime --request subtitles.json\nTransforms supplied SRT/VTT and actual segment maps to per-clip sidecars as JSON; no media tools required.")
		return err
	}
	if args[0] != "retime" {
		return invalidCLI(errors.New("subtitles requires retime"))
	}
	flags := flag.NewFlagSet("subtitles retime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	path := flags.String("request", "", "JSON request containing supplied text, format and actual output segment maps")
	timeout := flags.Duration("timeout", 2*time.Minute, "subtitle transformation timeout")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if *path == "" || flags.NArg() != 0 {
		return invalidCLI(errors.New("subtitles retime requires --request and named flags only"))
	}
	if *timeout <= 0 {
		return invalidCLI(errors.New("timeout must be positive"))
	}
	file, err := os.Open(*path)
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 4<<20 {
		return invalidCLI(errors.New("subtitle request exceeds 4 MiB"))
	}
	var request engine.SubtitleRetimeRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return invalidCLI(errors.New("invalid subtitle request"))
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalidCLI(errors.New("send exactly one subtitle request"))
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	result, err := engine.RetimeSubtitles(ctx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return invalidCLI(err)
	}
	return json.NewEncoder(stdout).Encode(result)
}
