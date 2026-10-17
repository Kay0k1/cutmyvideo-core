package mediaexport

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type copyJoinRange struct{ start, end int64 }
type copyJoinSegment struct {
	name       string
	start, end int64
	info       mediainfo.Info
	file       JoinedFile
}

func JoinCopy(ctx context.Context, c Config, inputs [][]Input, ranges []edit.Range, request edit.ExportRequest, out, dir string, limit int64, report func(int64), finalOutput bool, runtime JoinedRuntime) ([]edit.Segment, error) {
	var manifest strings.Builder
	manifest.WriteString("ffconcat version 1.0\n")
	var result []edit.Segment
	var elapsed int64
	var fingerprint mediainfo.Info
	var aggregate int64
	// Exact repeats from the pinned original local source can reference one
	// verified segment in the concat plan. Labels and output positions remain
	// independent; remote and separately staged inputs are never reused here.
	reused := make(map[copyJoinRange]copyJoinSegment)
	for i, r := range ranges {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		name := fmt.Sprintf("segment-%d.%s", i, request.Format)
		path := runtime.JoinPath(dir, name)
		var session SourceFence
		if len(inputs[i]) == 1 {
			session = runtime.PinnedSource(ctx, inputs[i][0])
		}
		var start, end int64
		var p mediainfo.Info
		var stat JoinedFile
		cached, hit := reused[copyJoinRange{r.StartMS, r.EndMS}]
		if hit && session != nil {
			e := session.Check()
			if e != nil {
				return nil, e
			}
			name, start, end, p = cached.name, cached.start, cached.end, cached.info
			stat, e = runtime.StatFile(runtime.JoinPath(dir, name))
			if e != nil {
				return nil, e
			}
			if !cached.file.Same(stat) {
				return nil, &mediainfo.Problem{Code: "source_changed", Message: "A copied interval changed before joining; try exporting again"}
			}
		} else {
			progress := func(ms int64) {
				if report != nil {
					report(elapsed + ms)
				}
			}
			videoRequest := request
			if !request.NoAudio {
				videoRequest.NoAudio = true
				videoRequest.AudioTrack = nil
			}
			videoPath := runtime.JoinPath(dir, fmt.Sprintf("video-%d.%s", i, request.Format))
			var e error
			start, end, e = Export(ctx, c, inputs[i], r, videoRequest, videoPath, progress, false, &p, runtime)
			remuxed := false
			if e == nil {
				selection, selectErr := runtime.SelectInputs(ctx, inputs[i], request)
				if selectErr != nil {
					e = selectErr
				} else if selection.Audio != nil {
					remuxed = true
					e = CopyAudio(ctx, c, inputs[i], selection, start, end, videoPath, path, request, min(limit, OutputBudget(c, r, request)), runtime)
				} else {
					e = runtime.Rename(videoPath, path)
				}
			}
			_ = runtime.Remove(videoPath)
			if e != nil {
				return nil, e
			}
			stat, e = runtime.StatFile(path)
			if e != nil {
				return nil, e
			}
			if remuxed {
				p, _, e = runtime.Probe(ctx, path, false)
				if e != nil {
					return nil, e
				}
			}
			if session != nil && len(reused) < 128 {
				reused[copyJoinRange{r.StartMS, r.EndMS}] = copyJoinSegment{name: name, start: start, end: end, info: p, file: stat}
			}
		}
		aggregate += stat.Size()
		if aggregate >= limit {
			return nil, ErrOutputLimit
		}
		if i == 0 {
			fingerprint = p
		} else if !SameJoinedStream(fingerprint.SelectedVideo(), p.SelectedVideo()) || !SameJoinedStream(fingerprint.SelectedAudio(), p.SelectedAudio()) {
			return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "Copied intervals have incompatible stream parameters; choose separate outputs or accurate mode"}
		}
		duration := end - start
		manifest.WriteString(FFConcatFile(name))
		fmt.Fprintf(&manifest, "duration %s\n", Seconds(duration))
		result = append(result, edit.Segment{Index: i, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputStartMS: elapsed, OutputEndMS: elapsed + duration})
		elapsed += duration
	}
	path := runtime.JoinPath(dir, "copy.ffconcat")
	if e := runtime.WritePlan(path, []byte(manifest.String())); e != nil {
		return nil, e
	}
	args := []string{"-hide_banner", "-v", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", InputFormats + ",concat", "-safe", "1", "-auto_convert", "0", "-copyts"}
	args = append(args, InputBounds(c.FFmpegThreads)...)
	args = append(args, "-i", path, "-map", "0", "-c", "copy", "-map_metadata", "-1", "-map_chapters", "-1", "-avoid_negative_ts", "disabled", "-fs", strconv.FormatInt(limit, 10))
	args = append(args, mediainfo.AudioMetadataArgs(fingerprint.SelectedAudio())...)
	if request.Format == "mp4" && finalOutput {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if e := runtime.Run(ctx, args, 0, nil); e != nil {
		return nil, runtime.CopyDiagnostic(e, request.Format)
	}
	if _, e := VerifyJoined(ctx, out, request, elapsed, limit, runtime); e != nil {
		return nil, e
	}
	if report != nil {
		report(elapsed)
	}
	return result, nil
}
