package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func runCutList(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(stdout, "Usage: cutmy cutlist import --format csv|ffmetadata --input cuts.txt [--plan --media recording.mp4 --duration-ms 60000]\n       cutmy cutlist export --format csv|ffmetadata (--input cut-list.json | --plan cuts.json | --segments segments.json)\nImport prints CutList or Plan JSON. Export prints JSON containing version, format and text; no media tools are used.")
		return err
	}
	command := args[0]
	if command != "import" && command != "export" {
		return invalidCLI(errors.New("cutlist requires import or export"))
	}
	f := flag.NewFlagSet("cutlist "+command, flag.ContinueOnError)
	f.SetOutput(stderr)
	format := f.String("format", "", "csv or ffmetadata")
	input := f.String("input", "", "UTF-8 text for import; CutList JSON for export")
	var makePlan bool
	var media, outputMode, planPath, segmentsPath string
	var duration int64
	if command == "import" {
		f.BoolVar(&makePlan, "plan", false, "print an editable Plan instead of CutList")
		f.StringVar(&media, "media", "", "local media path for the proposed Plan; not opened")
		f.Int64Var(&duration, "duration-ms", 0, "explicit source duration, required to expand a final marker")
		f.StringVar(&outputMode, "output-mode", "joined", "plan mode: joined or separate")
	} else {
		f.StringVar(&planPath, "plan", "", "existing version 1 Plan JSON; use its source intervals")
		f.StringVar(&segmentsPath, "segments", "", "actual Segment array JSON; use its output timestamps")
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return invalidCLI(err)
	}
	if f.NArg() != 0 || (*format != "csv" && *format != "ffmetadata") {
		return invalidCLI(errors.New("cutlist accepts named flags and requires --format csv|ffmetadata"))
	}
	if command == "import" {
		if *input == "" {
			return invalidCLI(errors.New("cutlist import requires --input"))
		}
		var planFlag bool
		f.Visit(func(v *flag.Flag) {
			if v.Name == "media" || v.Name == "duration-ms" || v.Name == "output-mode" {
				planFlag = true
			}
		})
		if !makePlan && planFlag {
			return invalidCLI(errors.New("--media, --duration-ms and --output-mode require --plan"))
		}
		if makePlan && (media == "" || duration <= 0) {
			return invalidCLI(errors.New("cutlist import --plan requires --media and positive --duration-ms"))
		}
		data, err := readCLIFile(*input, 1<<20)
		if err != nil {
			return err
		}
		list, err := engine.ParseCutList(*format, data)
		if err != nil {
			return err
		}
		if makePlan {
			absolute, pathErr := filepath.Abs(media)
			if pathErr != nil {
				return pathErr
			}
			plan, err := engine.BuildCutListPlan(absolute, list, duration, engine.AdvancedOptions{}, outputMode)
			if err != nil {
				return err
			}
			return json.NewEncoder(stdout).Encode(plan)
		}
		return json.NewEncoder(stdout).Encode(list)
	}
	count := 0
	for _, path := range []string{*input, planPath, segmentsPath} {
		if path != "" {
			count++
		}
	}
	if count != 1 {
		return invalidCLI(errors.New("cutlist export requires exactly one of --input, --plan or --segments"))
	}
	var list engine.CutList
	var err error
	switch {
	case planPath != "":
		plan, readErr := readCuttingPlan(planPath)
		if readErr != nil {
			return readErr
		}
		list, err = engine.CutListFromPlan(plan)
	case segmentsPath != "":
		var segments []engine.Segment
		if err := readCLIJSON(segmentsPath, &segments); err != nil {
			return err
		}
		list, err = engine.CutListFromSegments(segments)
	default:
		err = readCLIJSON(*input, &list)
	}
	if err != nil {
		return err
	}
	data, err := engine.EncodeCutList(*format, list)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		Version int    `json:"version"`
		Format  string `json:"format"`
		Text    string `json:"text"`
	}{1, *format, string(data)})
}

func readCLIFile(path string, limit int64) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, invalidCLI(errors.New("input must be a regular local file"))
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, invalidCLI(errors.New("input exceeds 1 MiB"))
	}
	return data, nil
}

func readCLIJSON(path string, target any) error {
	data, err := readCLIFile(path, 1<<20)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return invalidCLI(errors.New("send valid JSON with supported fields"))
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return invalidCLI(errors.New("send exactly one JSON value"))
	}
	return nil
}
