package main

import (
	"encoding/json"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// Release builders set these through -ldflags. Go-installed modules and local
// builds fall back to Go's embedded module/VCS information.
var version = "dev"
var commit = "unknown"
var buildTime = "unknown"

const usage = `cutmy — manually cut local media or run the self-hosted API

Usage:
  cutmy inspect --input recording.mp4
  cutmy clip --input recording.mp4 --start 00:01:00 --end 00:01:30 --output clip.mp4
  cutmy server                 Start the HTTP API (PostgreSQL required)
  cutmy worker                 Process queued exports
  cutmy maintenance            Run one bounded storage cleanup cycle
  cutmy healthcheck             Check API readiness
  cutmy worker-healthcheck      Check the worker health marker
  cutmy version                 Print JSON build information

Use cutmy <command> --help for flags. Local inspect and clip need FFmpeg/ffprobe,
but do not need a database. See https://github.com/Kay0k1/cutmyvideo-core
`

func printVersion(out io.Writer) error {
	v, revision, built := version, commit, buildTime
	if info, ok := debug.ReadBuildInfo(); ok {
		if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				if revision == "unknown" {
					revision = setting.Value
				}
			case "vcs.time":
				if built == "unknown" {
					built = setting.Value
				}
			case "vcs.modified":
				if setting.Value == "true" && version == "dev" && !strings.HasSuffix(v, "+dirty") {
					v += "+dirty"
				}
			}
		}
	}
	return json.NewEncoder(out).Encode(struct {
		Version   string `json:"version"`
		Commit    string `json:"commit"`
		BuildTime string `json:"build_time"`
		GoVersion string `json:"go_version"`
		OS        string `json:"os"`
		Arch      string `json:"arch"`
	}{v, revision, built, runtime.Version(), runtime.GOOS, runtime.GOARCH})
}
