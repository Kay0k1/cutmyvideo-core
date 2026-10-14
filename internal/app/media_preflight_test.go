package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func preflightTestRequest() ExportRequest {
	return ExportRequest{SourceID: "source-id", Format: "mp4", Quality: "best", CutMode: "accurate", OutputMode: "separate", SubtitleMode: "omit", Ranges: []Range{{StartMS: 500, EndMS: 1500, Label: "  clip  "}}}
}

func preflightTestInfo() probeInfo {
	return probeInfo{Streams: []probeStream{{Index: 0, CodecType: "video", CodecName: "h264", Width: 160, Height: 90, TimeBase: "1/12800", SampleAspectRatio: "1:1"}, {Index: 1, CodecType: "audio", CodecName: "aac", Channels: 2, SampleRate: "48000", TimeBase: "1/48000"}}}
}

func preflightTestConfig(t *testing.T) Config {
	t.Helper()
	name := "ffmpeg"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("an unchanged executable identity for injected tool queries"), 0700); err != nil {
		t.Fatal(err)
	}
	return Config{FFmpeg: path, MaxRanges: 128, MaxRangeMS: 3600000, MaxJobMS: 3600000}
}

func preflightTestLists(kind string) []byte {
	switch kind {
	case "-encoders":
		return []byte("Encoders:\n V..... = Video\n ------\n V..... libx264 H.264 encoder\n A..... aac AAC encoder\n A..... libmp3lame MP3 encoder\n S..... subrip SubRip encoder\n S..... mov_text MOV text encoder\n")
	case "-muxers":
		return []byte("Formats:\n .E. = Muxing supported\n ---\n E mp4 MP4\n E matroska Matroska\n E mp3 MP3\n E srt SubRip\n")
	case "-demuxers":
		return []byte("File formats:\n D. = Demuxing supported\n --\n D concat concat input\n D mov MOV input\n D matroska,webm Matroska input\n D srt SubRip input\n")
	case "-filters":
		return []byte("Filters:\n T.. = Timeline support\n ------\n .. scale V->V Scale video\n .. setsar V->V Set SAR\n .. crop V->V Crop video\n T. select V->N Select video\n T. aselect A->N Select audio\n .. aresample A->A Resample audio\n .. zscale V->V Scale color\n .. format V->V Pixel format\n .. setparams V->V Set colors\n .. tonemap V->V Tone map\n")
	case "-bsfs":
		return []byte("Bitstream filters:\nh264_metadata\nnoise\n")
	default:
		return nil
	}
}

func preflightTestQuery(_ context.Context, _ string, args ...string) ([]byte, error) {
	return preflightTestLists(args[len(args)-1]), nil
}

func TestPreflightCapabilitiesParseExactLists(t *testing.T) {
	for _, tc := range []struct{ kind, exact, absent string }{
		{"encoders", "libx264", "x264"}, {"muxers", "mp4", "mov"}, {"demuxers", "webm", "web"}, {"filters", "setsar", "set"}, {"bsfs", "h264_metadata", "h264"},
	} {
		entries, ok := parsePreflightCapabilityList(tc.kind, preflightTestLists("-"+tc.kind))
		if !ok || !entries[tc.exact] || entries[tc.absent] {
			t.Fatalf("%s exact-name parsing: %v %v", tc.kind, entries, ok)
		}
	}
	for _, data := range []string{
		"libx264 is available", "Encoders:\n V..... libx264 missing separator", "Encoders:\n ------\n V..... different description mentions libx264", "Encoders:\n ------\n V..... libx264", "Encoders:\n ------\n ??? libx264 malformed flags", "Encoders:\n ------\n V..... libx264 valid\n truncated", "Encoders:\n ------\n V..... ../../libx264 invalid name",
	} {
		entries, ok := parsePreflightCapabilityList("encoders", []byte(data))
		if ok && entries["libx264"] {
			t.Fatalf("unverified/substring response passed: %q", data)
		}
	}
	if entries, ok := parsePreflightCapabilityList("filters", []byte("Filters:\n ------\n TSC scale V->V Legacy three flags\n")); !ok || !entries["scale"] {
		t.Fatal("legacy three-flag filter list was rejected")
	}
	legacyFilters := "Filters:\n  T.. = Timeline support\n  .S. = Slice threading\n  ..C = Command support\n  A = Audio input/output\n  V = Video input/output\n  N = Dynamic number and/or type of input/output\n  | = Source or sink filter\n ... scale V->V Scale video; description mentions tonemap\n ... setsar V->V Set SAR\n"
	entries, ok := parsePreflightCapabilityList("filters", []byte(legacyFilters))
	if !ok || !entries["scale"] || !entries["setsar"] || entries["tonemap"] || entries["A"] || entries["T.."] {
		t.Fatalf("FFmpeg 5.1 filter table or legend/descriptive name isolation failed: %v %v", entries, ok)
	}
	for _, malformed := range []string{
		"Filters:\n T.. = Timeline support\n ... scale V->V Missing the rest of the legacy legend\n",
		strings.Replace(legacyFilters, "| = Source or sink filter", "unrecognized legend mentioning scale", 1),
		legacyFilters + "description only mentioning crop\n",
	} {
		if _, ok := parsePreflightCapabilityList("filters", []byte(malformed)); ok {
			t.Fatalf("malformed separator-free table passed: %q", malformed)
		}
	}
	entries, ok = parsePreflightCapabilityList("demuxers", []byte("File formats:\n D. = Demuxing supported\n .E = Muxing supported\n --\n D concat Virtual concatenation\n D libcdio\n"))
	if !ok || !entries["concat"] || !entries["libcdio"] {
		t.Fatalf("FFmpeg 5.1 demuxer with no description failed: %v %v", entries, ok)
	}
	if _, ok := parsePreflightCapabilityList("demuxers", []byte("File formats:\n --\n D\n")); ok {
		t.Fatal("demuxer row with no name passed")
	}
	if _, ok := parsePreflightCapabilityList("encoders", []byte(strings.Repeat("x", preflightCapabilityBytes+1))); ok {
		t.Fatal("oversized list passed")
	}
	for _, tc := range []struct {
		selection mediaSelection
		request   ExportRequest
		missing   string
		code      string
	}{
		{mediaSelection{Video: &probeStream{}, Audio: &probeStream{}}, ExportRequest{Format: "mp4", CutMode: "accurate"}, "libx264", "encoder_unavailable"},
		{mediaSelection{Video: &probeStream{}}, ExportRequest{Format: "mp4", CutMode: "accurate"}, "setsar", "filter_unavailable"},
		{mediaSelection{Video: &probeStream{}}, ExportRequest{Format: "mp4", CutMode: "copy"}, "mp4", "muxer_unavailable"},
		{mediaSelection{Video: &probeStream{}}, ExportRequest{Format: "mp4", CutMode: "copy", OutputMode: "joined"}, "concat", "demuxer_unavailable"},
	} {
		result := PreflightResult{}
		err := checkPreflightCapabilities(context.Background(), preflightTestConfig(t), preflightRequiredCapabilities(tc.selection, tc.request, nil, nil), &result, func(ctx context.Context, path string, args ...string) ([]byte, error) {
			data, _ := preflightTestQuery(ctx, path, args...)
			var kept []string
			for _, line := range strings.Split(string(data), "\n") {
				fields := strings.Fields(line)
				if len(fields) > 1 && fields[1] == tc.missing {
					continue
				}
				kept = append(kept, line)
			}
			return []byte(strings.Join(kept, "\n")), nil
		})
		var problem *sourceProblem
		if !errors.As(err, &problem) || problem.code != tc.code {
			t.Fatalf("missing %s: expected %s, got %v", tc.missing, tc.code, err)
		}
	}
}

func TestPreflightCapabilitiesFailuresAreBoundedAndCancelable(t *testing.T) {
	// The same native test executable acts as a portable oversized tool. It
	// needs no shell, environment override or platform-specific executable.
	if len(os.Args) > 0 && os.Args[len(os.Args)-1] == "cutmy-preflight-large-response" {
		_, _ = os.Stdout.Write([]byte(strings.Repeat("x", preflightCapabilityBytes+1)))
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := queryPreflightCapabilities(ctx, executable, "-test.run=^TestPreflightCapabilitiesFailuresAreBoundedAndCancelable$", "--", "cutmy-preflight-large-response"); !errors.Is(err, errProcessResponseTooLarge) {
		t.Fatalf("oversized tool response did not stop: %v", err)
	}
	groups := []preflightCapabilityGroup{{listing: "encoders", code: "encoder_unavailable", required: []string{"libx264"}}}
	for _, failure := range []string{"timeout", "malformed", "oversized", "failed"} {
		result := PreflightResult{}
		err := checkPreflightCapabilities(context.Background(), preflightTestConfig(t), groups, &result, func(queryCtx context.Context, _ string, _ ...string) ([]byte, error) {
			deadline, ok := queryCtx.Deadline()
			if !ok || time.Until(deadline) > 2*time.Second {
				t.Fatal("capability query has no two-second bound")
			}
			switch failure {
			case "timeout":
				return nil, context.DeadlineExceeded
			case "oversized":
				return []byte(strings.Repeat("x", preflightCapabilityBytes+1)), nil
			case "failed":
				return preflightTestLists("-encoders"), errors.New("failed diagnostic containing private-file-name")
			default:
				return []byte("unknown capability response"), nil
			}
		})
		if err != nil || len(result.Checks) != 1 || result.Checks[0].Status != "deferred" || len(result.Warnings) != 1 || result.Warnings[0].Code != "tool_capabilities_unverified" || strings.Contains(result.Warnings[0].Message, "private-file-name") {
			t.Fatalf("%s was not explicitly deferred: %+v %v", failure, result, err)
		}
	}
	caller, stop := context.WithCancel(context.Background())
	if err := checkPreflightCapabilities(caller, preflightTestConfig(t), groups, &PreflightResult{}, func(context.Context, string, ...string) ([]byte, error) {
		stop()
		return preflightTestLists("-encoders"), nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation was swallowed: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing-ffmpeg")
	err = checkPreflightCapabilities(ctx, Config{FFmpeg: missing}, groups, &PreflightResult{}, queryPreflightCapabilities)
	var pathError *os.PathError
	var execError *exec.Error
	if !errors.As(err, &pathError) && !errors.As(err, &execError) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing tool lost its operating-system cause: %v", err)
	}
	c := preflightTestConfig(t)
	err = checkPreflightCapabilities(ctx, c, groups, &PreflightResult{}, func(queryCtx context.Context, path string, args ...string) ([]byte, error) {
		if err := os.WriteFile(path, []byte("replacement executable identity of another length"), 0700); err != nil {
			t.Fatal(err)
		}
		return preflightTestQuery(queryCtx, path, args...)
	})
	var problem *sourceProblem
	if !errors.As(err, &problem) || problem.code != "tool_unavailable" {
		t.Fatalf("changed tool certified its mixed capability snapshot: %v", err)
	}
}

func TestPreflightLocalRejectsChangingInput(t *testing.T) {
	for _, changeAt := range []string{"inspection", "capabilities"} {
		for _, mutation := range []string{"content", "identity"} {
			t.Run(changeAt+"/"+mutation, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "source.mp4")
				if err := os.WriteFile(path, []byte("original source"), 0600); err != nil {
					t.Fatal(err)
				}
				before, err := preflightFileIdentity(path)
				if err != nil {
					t.Fatal(err)
				}
				changed := false
				change := func() {
					if changed {
						return
					}
					changed = true
					if mutation == "identity" {
						replacement := path + ".replacement"
						if err := os.WriteFile(replacement, []byte("original source"), 0600); err != nil {
							t.Fatal(err)
						}
						if err := os.Chtimes(replacement, before.ModTime(), before.ModTime()); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.Rename(replacement, path); err != nil {
							t.Fatal(err)
						}
						return
					}
					if err := os.WriteFile(path, []byte("changed source with a different length"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				inspect := func(context.Context, Config, string, bool) (probeInfo, int64, error) {
					if changeAt == "inspection" {
						change()
					}
					return preflightTestInfo(), 2000, nil
				}
				query := func(ctx context.Context, path string, args ...string) ([]byte, error) {
					if changeAt == "capabilities" {
						change()
					}
					return preflightTestQuery(ctx, path, args...)
				}
				_, err = preflightLocalWithQueries(context.Background(), preflightTestConfig(t), path, preflightTestRequest(), inspect, query)
				var problem *sourceProblem
				if !errors.As(err, &problem) || problem.code != "source_changed" {
					t.Fatalf("input changed during %s: %v", changeAt, err)
				}
			})
		}
	}
	path := filepath.Join(t.TempDir(), "source.mp4")
	if err := os.WriteFile(path, []byte("source"), 0600); err != nil {
		t.Fatal(err)
	}
	request := preflightTestRequest()
	result, err := preflightLocalWithQueries(context.Background(), preflightTestConfig(t), path, request, func(context.Context, Config, string, bool) (probeInfo, int64, error) {
		return preflightTestInfo(), 2000, nil
	}, preflightTestQuery)
	if err != nil || !result.Valid || result.Request.Ranges[0].Label != "clip" || request.Ranges[0].Label != "  clip  " || result.Request.SourceID != request.SourceID || result.Request.OutputMode != "" || result.Request.SubtitleMode != "" {
		t.Fatalf("normalization was not privately reported: %+v %v original=%+v", result, err, request)
	}
	for _, tc := range []struct {
		name string
		code string
		edit func(*probeInfo, *ExportRequest)
	}{
		{"audio_missing", "audio_missing", func(p *probeInfo, r *ExportRequest) { p.Streams = p.Streams[:1]; r.Format = "mp3" }},
		{"video_missing", "unsupported_media", func(p *probeInfo, _ *ExportRequest) { p.Streams = p.Streams[1:] }},
		{"audio_index", "audio_track_unavailable", func(_ *probeInfo, r *ExportRequest) { index := 99; r.AudioTrack = &index }},
		{"bitmap_subtitle", "subtitle_unsupported", func(p *probeInfo, r *ExportRequest) {
			p.Streams = append(p.Streams, probeStream{Index: 2, CodecType: "subtitle", CodecName: "dvd_subtitle"})
			r.Format, r.CutMode, r.SubtitleMode = "mkv", "copy", "preserve"
		}},
		{"copy_resize", "copy_quality_unsupported", func(p *probeInfo, r *ExportRequest) {
			p.Streams[0].Width, p.Streams[0].Height = 1920, 1080
			r.CutMode, r.Quality = "copy", "720p"
		}},
		{"tiny_crop", "invalid_argument", func(_ *probeInfo, r *ExportRequest) { r.Crop = &Crop{Width: .001, Height: .001} }},
		{"invalid_timebase", "unsupported_media", func(p *probeInfo, _ *ExportRequest) { p.Streams[0].TimeBase = "0/0" }},
		{"unsupported_rotation", "unsupported_media", func(p *probeInfo, _ *ExportRequest) {
			rotation := 45.0
			p.Streams[0].SideDataList = []probeSideData{{Rotation: &rotation}}
		}},
		{"incomplete_hdr", "unsupported_media", func(p *probeInfo, _ *ExportRequest) { p.Streams[0].ColorTransfer = "smpte2084" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info, request := preflightTestInfo(), preflightTestRequest()
			tc.edit(&info, &request)
			queries := 0
			_, err := preflightLocalWithQueries(context.Background(), preflightTestConfig(t), path, request, func(context.Context, Config, string, bool) (probeInfo, int64, error) {
				return info, 2000, nil
			}, func(ctx context.Context, path string, args ...string) ([]byte, error) {
				queries++
				return preflightTestQuery(ctx, path, args...)
			})
			if got := LocalErrorCode(err); got != tc.code || queries != 0 {
				t.Fatalf("known source problem should fail before capability queries: got %s (%v), want %s, queries %d", got, err, tc.code, queries)
			}
		})
	}
	info := preflightTestInfo()
	info.Streams[0].ColorTransfer, info.Streams[0].ColorPrimaries, info.Streams[0].ColorSpace = "smpte2084", "bt2020", "bt2020nc"
	result, err = preflightLocalWithQueries(context.Background(), preflightTestConfig(t), path, preflightTestRequest(), func(context.Context, Config, string, bool) (probeInfo, int64, error) {
		return info, 2000, nil
	}, preflightTestQuery)
	if err != nil || result.Video == nil || !result.Video.HDRToSDR {
		t.Fatalf("HDR rendering requirements were not reported: %+v %v", result, err)
	}
}

func TestPreflightLocalChecksMediaWithoutRendering(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	input := filepath.Join(c.DataDir, "source.mp4")
	if _, err := runCommand(ctx, c.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=2", "-f", "lavfi", "-i", "sine=sample_rate=48000:duration=2", "-c:v", "libx264", "-threads", "1", "-c:a", "aac", input); err != nil {
		t.Fatal(err)
	}
	commands := countMediaCommands(t, &c)
	for _, format := range []string{"mp4", "mp3", "mkv"} {
		request := preflightTestRequest()
		request.Format = format
		request.OutputMode = "joined"
		if format == "mkv" {
			request.CutMode = "copy"
		}
		result, err := PreflightLocal(ctx, c, input, request)
		if err != nil || !result.Valid || result.Source.DurationMS != 2000 || result.RequestedMS != 1000 || result.OutputCount != 1 || len(result.TrackManifest) != 2 {
			t.Fatalf("%s preflight: %+v %v", format, result, err)
		}
		if (result.Video == nil) != (format == "mp3") || result.Video != nil && (result.Video.Width != 160 || result.Video.Height != 90) {
			t.Fatalf("%s output geometry: %+v", format, result.Video)
		}
		var copied bool
		for _, warning := range result.Warnings {
			if warning.Code == "copy_container_unverified" {
				copied = true
			}
			if warning.Code == "tool_capabilities_unverified" {
				t.Fatalf("native %s table unexpectedly unverified: %+v", format, result)
			}
		}
		if copied != (format == "mkv") {
			t.Fatalf("copy compatibility was incorrectly promised: %+v", result)
		}
		expected := []string{"tool_muxers", "tool_demuxers"}
		if format != "mkv" {
			expected = append(expected, "tool_encoders", "tool_filters")
		}
		for _, name := range expected {
			passed := false
			for _, check := range result.Checks {
				if check.Name == name && check.Status == "passed" {
					passed = true
				}
			}
			if !passed {
				t.Fatalf("native %s prerequisite %s was not verified: %+v", format, name, result.Checks)
			}
		}
	}
	request := preflightTestRequest()
	request.Crop = &Crop{X: .25, Width: .5, Height: 1}
	result, err := PreflightLocal(ctx, c, input, request)
	if err != nil || result.Video == nil || result.Video.Width != 80 || result.Video.Height != 90 {
		t.Fatalf("crop geometry: %+v %v", result.Video, err)
	}
	for _, line := range commands() {
		if strings.HasPrefix(line, "ffmpeg ") {
			if !strings.Contains(line, "-encoders") && !strings.Contains(line, "-muxers") && !strings.Contains(line, "-filters") && !strings.Contains(line, "-demuxers") && !strings.Contains(line, "-bsfs") {
				t.Fatalf("preflight rendered or staged media: %s", line)
			}
			if strings.Contains(line, " -i ") {
				t.Fatalf("preflight launched an input-processing FFmpeg: %s", line)
			}
		}
		if strings.Contains(line, "-show_packets") || strings.Contains(line, "-show_frames") {
			t.Fatalf("preflight scanned frames/keyframes: %s", line)
		}
	}
	entries, err := os.ReadDir(c.DataDir)
	if err != nil || len(entries) != 1 || entries[0].Name() != "source.mp4" {
		t.Fatalf("preflight created media/workspace files: %v %v", entries, err)
	}
}
