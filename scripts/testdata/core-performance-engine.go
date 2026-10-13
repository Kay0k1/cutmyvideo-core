// A standalone witness of the public embedding API. Build this same source from
// each immutable module checkout; it is never installed as a production command.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func main() {
	planFile := flag.String("plan", "", "frozen cutting plan")
	output := flag.String("output", "", "owned output")
	flag.Parse()
	if *planFile == "" || *output == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "plan and output are required")
		os.Exit(2)
	}
	file, err := os.Open(*planFile)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	var plan engine.Plan
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		panic(err)
	}
	e := engine.New(engine.Config{FFmpegPath: os.Getenv("FFMPEG_PATH"), FFprobePath: os.Getenv("FFPROBE_PATH"), EncodeProfile: "fast", FFmpegThreads: 2})
	var result engine.BatchResult
	if len(plan.Ranges) == 1 {
		r := plan.Ranges[0]
		single, err := e.ExportWithOptions(context.Background(), plan.Input, *output, engine.Range{StartMS: r.StartMS, EndMS: r.EndMS}, plan.Options)
		if err != nil {
			panic(err)
		}
		result.Outputs = []engine.BatchOutput{{Result: single.Result, Label: r.Label, Segments: single.Segments, TrackManifest: single.TrackManifest}}
	} else {
		result, err = e.ExecutePlan(context.Background(), plan, *output)
		if err != nil {
			panic(err)
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
		panic(err)
	}
}
