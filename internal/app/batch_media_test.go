package app

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func batchFixture(t *testing.T, c Config, ctx context.Context) string {
	t.Helper()
	srt := filepath.Join(c.DataDir, "source.srt")
	if e := os.WriteFile(srt, []byte("1\n00:00:00,500 --> 00:00:02,800\nAlpha\n\n2\n00:00:03,200 --> 00:00:05,500\nBeta\n\n"), 0600); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(c.DataDir, "multitrack.mp4")
	args := []string{"-v", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25", "-f", "lavfi", "-i", "sine=frequency=220:sample_rate=48000", "-f", "lavfi", "-i", "aevalsrc=sin(2*PI*(800*t+20*t*t)):s=48000", "-i", srt, "-map", "0:v", "-map", "1:a", "-map", "2:a", "-map", "3:s", "-t", "6", "-c:v", "libx264", "-threads", "1", "-g", "25", "-keyint_min", "25", "-sc_threshold", "0", "-bf", "2", "-c:a", "aac", "-c:s", "mov_text", "-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=rus", "-metadata:s:a:1", "title=Russian", "-disposition:a:0", "default", "-disposition:a:1", "0", path}
	if _, e := runCommand(ctx, c.FFmpeg, args...); e != nil {
		t.Fatal(e)
	}
	return path
}

func TestBatchAudioSelectionSubtitlesAndJoinedTimeline(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	input := batchFixture(t, c, ctx)
	inspected, e := InspectLocal(ctx, c, input)
	if e != nil {
		t.Fatal(e)
	}
	if len(inspected.Tracks) != 4 || inspected.Tracks[2].Language != "rus" || inspected.Tracks[2].SampleRate != "48000" {
		t.Fatalf("inspection tracks: %+v", inspected.Tracks)
	}
	selected := 2
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "accurate", AudioTrack: &selected}
	for _, mode := range []string{"accurate", "copy"} {
		for _, format := range []string{"mp4", "mkv"} {
			if mode == "accurate" && format == "mkv" {
				continue
			}
			t.Run(mode+"-"+format, func(t *testing.T) {
				options := request
				options.CutMode, options.Format = mode, format
				if mode == "copy" {
					options.SubtitleMode = "preserve"
				}
				ranges := []Range{{StartMS: 3200, EndMS: 4600, Label: "late"}, {StartMS: 400, EndMS: 1600, Label: "early"}}
				out := filepath.Join(c.DataDir, mode+"-"+format+"."+format)
				segments, e := ExportJoinedLocal(ctx, c, input, out, ranges, options)
				if e != nil {
					t.Fatal(e)
				}
				if len(segments) != 2 || segments[0].SourceStartMS <= segments[1].SourceStartMS || segments[1].OutputStartMS != segments[0].OutputEndMS {
					t.Fatalf("joined segments: %+v", segments)
				}
				if _, e = runCommand(ctx, c.FFmpeg, "-v", "error", "-xerror", "-i", out, "-f", "null", "-"); e != nil {
					t.Fatalf("independent decode: %v", e)
				}
				p, _, e := probe(ctx, c, out, false)
				if e != nil {
					t.Fatal(e)
				}
				if audio := p.selectedAudio(); audio == nil || audio.Tags.Language != "rus" {
					t.Fatalf("selected audio metadata lost: %+v", audio)
				}
				for _, segment := range segments {
					compareAudioTimeline(t, ctx, c, input, out, selected, segment.SourceStartMS+300, segment.OutputStartMS+300, mode == "copy")
				}
				if mode == "copy" {
					data, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", out, "-map", "0:s:0", "-c:s", "srt", "-f", "srt", "-")
					if e != nil {
						t.Fatal(e)
					}
					cues, e := parseSRT(data)
					if e != nil {
						t.Fatal(e)
					}
					var beta, alpha *subtitleCue
					for i := range cues {
						if strings.Contains(cues[i].Text, "Beta") {
							beta = &cues[i]
						}
						if strings.Contains(cues[i].Text, "Alpha") {
							alpha = &cues[i]
						}
					}
					if beta == nil || alpha == nil || beta.StartMS >= alpha.StartMS || beta.EndMS > segments[0].OutputEndMS+2 || alpha.StartMS < segments[1].OutputStartMS {
						t.Fatalf("clipped/rebased subtitles: %s; segments %+v", data, segments)
					}
				}
			})
		}
	}
	// Preview and export share explicit selection, including silence.
	for _, silent := range []bool{false, true} {
		options := request
		if silent {
			options.AudioTrack = nil
			options.NoAudio = true
		}
		out := filepath.Join(c.DataDir, "preview-"+strconv.FormatBool(silent)+".mp4")
		if e := renderPreviewOptions(ctx, c, []mediaInput{{Path: input}}, Range{EndMS: 1200}, out, options); e != nil {
			t.Fatal(e)
		}
		p, _, e := probe(ctx, c, out, false)
		if e != nil {
			t.Fatal(e)
		}
		if (p.selectedAudio() == nil) != silent {
			t.Fatal("preview ignored audio selection")
		}
		if !silent {
			compareAudioTimeline(t, ctx, c, input, out, selected, 300, 300, false)
		}
	}
	if previewCacheKeyOptions(Source{ID: "source"}, 0, request) == previewCacheKey(Source{ID: "source"}, 0) || previewCacheKeyOptions(Source{ID: "source"}, 0, ExportRequest{NoAudio: true}) == previewCacheKey(Source{ID: "source"}, 0) {
		t.Fatal("audio selection aliases preview cache")
	}
}

func compareAudioTimeline(t *testing.T, ctx context.Context, c Config, input, out string, index int, sourceStart, outputStart int64, copyMode bool) {
	t.Helper()
	decode := func(path string, track int, start int64) []byte {
		filter := "aresample=async=1000:min_hard_comp=0.001:first_pts=0,atrim=start=" + seconds(start) + ":end=" + seconds(start+300) + ",asetpts=PTS-STARTPTS"
		pcm, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-copyts", "-i", path, "-map", "0:"+strconv.Itoa(track), "-af", filter, "-ac", "1", "-ar", "48000", "-f", "s16le", "-")
		if e != nil {
			t.Fatal(e)
		}
		return pcm
	}
	reference, actual := decode(input, index, sourceStart), decode(out, 1, outputStart)
	count := min(len(reference), len(actual)) / 2
	if count < 10000 {
		t.Fatalf("audio duration too short: %d", count)
	}
	best, bestLag := -1.0, 0
	for lag := -2400; lag <= 2400; lag++ {
		var xy, xx, yy float64
		for i := 2400; i < count-2400; i += 8 {
			x := float64(int16(binary.LittleEndian.Uint16(reference[i*2:])))
			y := float64(int16(binary.LittleEndian.Uint16(actual[(i+lag)*2:])))
			xy += x * y
			xx += x * x
			yy += y * y
		}
		if score := xy / math.Sqrt(xx*yy); score > best {
			best, bestLag = score, lag
		}
	}
	threshold, lagLimit := 0.95, 1200
	if copyMode {
		threshold, lagLimit = 0.99, 48
	}
	if best < threshold || int(math.Abs(float64(bestLag))) > lagLimit {
		t.Fatalf("audio selection/alignment changed: correlation %.5f lag %d samples at source=%d output=%d", best, bestLag, sourceStart, outputStart)
	}
}

func TestCopyContainerAndUnsupportedTrackDiagnostics(t *testing.T) {
	c := mediaConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	input := batchFixture(t, c, ctx)
	bad := 0
	request := ExportRequest{Format: "mp4", Quality: "best", CutMode: "copy", AudioTrack: &bad}
	if _, _, e := ExportLocal(ctx, c, input, filepath.Join(c.DataDir, "invalid.mp4"), Range{EndMS: 1000}, request); LocalErrorCode(e) != "audio_track_unavailable" {
		t.Fatalf("non-audio index accepted: %v", e)
	}
	pcm := filepath.Join(c.DataDir, "pcm.mkv")
	if _, e := runCommand(ctx, c.FFmpeg, "-v", "error", "-i", input, "-map", "0", "-c:v", "copy", "-c:a", "pcm_s16le", "-c:s", "srt", "-metadata:s:a:1", "title=Russian", pcm); e != nil {
		t.Fatal(e)
	}
	inspected, e := InspectLocal(ctx, c, pcm)
	if e != nil {
		t.Fatal(e)
	}
	if inspected.Tracks[2].Title != "Russian" {
		t.Fatal("MKV track title missing")
	}
	selected := 2
	request.AudioTrack = &selected
	if _, _, e = ExportLocal(ctx, c, pcm, filepath.Join(c.DataDir, "incompatible.mp4"), Range{EndMS: 1000}, request); LocalErrorCode(e) != "copy_incompatible" {
		t.Fatalf("PCM silently changed in MP4: %v", e)
	}
	request.Format = "mkv"
	request.SubtitleMode = "preserve"
	out := filepath.Join(c.DataDir, "preserved.mkv")
	if _, _, e = ExportLocal(ctx, c, pcm, out, Range{StartMS: 100, EndMS: 1600}, request); e != nil {
		t.Fatal(e)
	}
	p, _, e := probe(ctx, c, out, false)
	if e != nil || p.selectedAudio().CodecName != "pcm_s16le" {
		t.Fatalf("MKV failed to preserve PCM: %+v %v", p, e)
	}
	styled := filepath.Join(c.DataDir, "styled.mkv")
	if _, e = runCommand(ctx, c.FFmpeg, "-v", "error", "-i", input, "-map", "0", "-c:v", "copy", "-c:a", "copy", "-c:s", "ass", styled); e != nil {
		t.Fatal(e)
	}
	if _, _, e = ExportLocal(ctx, c, styled, filepath.Join(c.DataDir, "styled-result.mkv"), Range{EndMS: 1000}, request); LocalErrorCode(e) != "subtitle_unsupported" {
		t.Fatalf("styled subtitles silently changed: %v", e)
	}
}
