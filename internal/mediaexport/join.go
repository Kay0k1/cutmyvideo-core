package mediaexport

import (
	"context"
	"fmt"
	"math"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

func ExportJoined(ctx context.Context, c Config, inputs [][]Input, ranges []edit.Range, request edit.ExportRequest, out string, report func(int64), runtime JoinedRuntime) ([]edit.Segment, error) {
	if err := edit.ValidateCropMode(request); err != nil {
		return nil, &mediainfo.Problem{Code: "crop_invalid", Message: err.Error()}
	}
	if len(ranges) == 0 || len(inputs) != len(ranges) {
		return nil, fmt.Errorf("joined export needs inputs for every range")
	}
	dir, e := runtime.NewWorkspace(runtime.BaseDir(out), ".joined-")
	if e != nil {
		return nil, e
	}
	defer runtime.RemoveAll(dir)
	selections := make([]Selection, len(ranges))
	for i, r := range ranges {
		if r.EndMS <= r.StartMS {
			return nil, fmt.Errorf("range %d must end after its start", i+1)
		}
		selections[i], e = runtime.SelectInputs(ctx, inputs[i], request)
		if e != nil {
			return nil, e
		}
		if request.Format != "mp3" && selections[i].Video == nil {
			return nil, mediainfo.ErrUnsupported
		}
		if request.Format == "mp3" && selections[i].Audio == nil {
			return nil, ErrNoAudio
		}
		if len(inputs[i]) != len(inputs[0]) || !SameJoinedStream(selections[0].Video, selections[i].Video) || !SameJoinedStream(selections[0].Audio, selections[i].Audio) {
			return nil, &mediainfo.Problem{Code: "join_incompatible", Message: "Joined intervals must use compatible streams from the same source; choose separate outputs"}
		}
	}
	request.Ranges = ranges
	limit := JoinedOutputBudget(c, request)
	media := out
	var subtitleSource mediainfo.Info
	if request.SubtitleMode == "preserve" {
		if len(inputs[0]) != 1 || inputs[0][0].Remote {
			return nil, &mediainfo.Problem{Code: "subtitle_unavailable", Message: "Preserving subtitles requires a local source"}
		}
		p, err := runtime.ProbeStreams(ctx, inputs[0][0].Path, false)
		if err != nil {
			return nil, err
		}
		if _, err = mediainfo.TrackManifest(p, selections[0].Video, selections[0].Audio, request); err != nil {
			return nil, err
		}
		subtitleSource = p
		media = runtime.JoinPath(dir, "media."+request.Format)
	}
	var segments []edit.Segment
	if request.CutMode == "copy" {
		segments, e = JoinCopy(ctx, c, inputs, ranges, request, media, dir, limit, report, media == out, runtime)
	} else {
		segments, e = JoinAccurate(ctx, c, inputs, ranges, request, media, dir, limit, selections[0], report, media == out, runtime)
	}
	if e != nil {
		return nil, e
	}
	if request.SubtitleMode == "preserve" {
		e = runtime.MuxSubtitles(ctx, inputs[0][0].Path, subtitleSource, media, out, segments, request, limit)
		if e != nil {
			return nil, e
		}
	}
	return segments, nil
}

func VerifyJoined(ctx context.Context, path string, request edit.ExportRequest, expected, limit int64, runtime Runtime) (mediainfo.Info, error) {
	size, e := runtime.StatSize(path)
	if e != nil {
		return mediainfo.Info{}, e
	}
	if size >= limit {
		return mediainfo.Info{}, ErrOutputLimit
	}
	p, duration, e := runtime.Probe(ctx, path, false)
	if e != nil {
		return p, e
	}
	if request.Format != "mp3" && p.SelectedVideo() == nil {
		return p, mediainfo.ErrUnsupported
	}
	if request.Format == "mp3" && p.SelectedAudio() == nil {
		return p, ErrNoAudio
	}
	if math.Abs(float64(duration-expected)) > 350 {
		return p, fmt.Errorf("joined result duration does not match its selected intervals")
	}
	return p, nil
}
