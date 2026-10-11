package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// hmcStage probes the exact downloaded init/segment bytes independently before
// the application remuxes them. Container generation losing a tag must not be
// mistaken for successful metadata preservation by the staging implementation.
func hmcStage(t *testing.T, ctx context.Context, c Config, input, kind, name string, prepare ...func(string)) (string, int64, []mcStream) {
	t.Helper()
	dir := filepath.Join(c.DataDir, name+"-hls")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ext := "ts"
	if kind == "fmp4" {
		ext = "m4s"
	}
	// The HLS muxer derives the init-map directory from URL slash separators.
	// Native Windows backslashes put init.mp4 in the process working directory;
	// use slash paths for FFmpeg while filesystem operations keep native paths.
	mcRun(t, ctx, c, "-i", filepath.ToSlash(input), "-map", "0", "-c", "copy", "-f", "hls", "-hls_time", "1", "-hls_playlist_type", "vod", "-hls_segment_type", kind, "-hls_segment_filename", filepath.ToSlash(filepath.Join(dir, "%02d."+ext)), filepath.ToSlash(filepath.Join(dir, "index.m3u8")))
	if kind == "fmp4" {
		// FFmpeg 5.1's nested HLS muxer drops tkhd display matrices. Construct
		// the same valid fMP4 presentation on old and current fixture tools;
		// independent probing below must verify the actual init/segment bytes.
		hmcPreserveFixtureMatrix(t, input, filepath.Join(dir, "init.mp4"))
	}
	for _, update := range prepare {
		update(dir)
	}
	g, _ := fixtureGuard(t, dir)
	format := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Ext: "mp4"}
	playlist, err := loadHLS(ctx, g, format, "best", 0)
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	mapURL := ""
	for _, segment := range playlist.Segments {
		if segment.MapURL != "" && segment.MapURL != mapURL {
			bytes, err := os.ReadFile(filepath.Join(dir, filepath.Base(segment.MapURL)))
			if err != nil {
				t.Fatal(err)
			}
			raw = append(raw, bytes...)
			mapURL = segment.MapURL
		}
		bytes, err := os.ReadFile(filepath.Join(dir, filepath.Base(segment.URL)))
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, bytes...)
	}
	joined := filepath.Join(c.DataDir, name+"-input.media")
	if err := os.WriteFile(joined, raw, 0600); err != nil {
		t.Fatal(err)
	}
	streams := mcProbe(t, ctx, c, joined)
	staging := t.TempDir()
	path, offset, err := stageHLS(ctx, c, g, format, playlist, Range{EndMS: 2000}, staging, 0)
	if err != nil {
		t.Fatalf("actual %s staging: %v", kind, err)
	}
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(path) {
		t.Fatalf("staging retained raw/temporary files: %v, %v", entries, err)
	}
	return path, offset, streams
}

// FFmpeg's HLS muxer does not copy audio disposition into its nested MP4
// initialization muxer. Set the standard tkhd track-enabled bit explicitly in
// this synthetic init map, then require independent ffprobe and decoded tones
// to prove that the resulting real fMP4 has the intended second default track.
func hmcDefaultSecondAudio(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, "init.mp4")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	audio := 0
	for _, track := range hmcMP4Tracks(t, raw) {
		if track.handler == "soun" {
			if len(track.header) < 4 {
				t.Fatal("audio fixture missing track header")
			}
			track.header[3] &^= 1
			if audio == 1 {
				track.header[3] |= 1
			}
			audio++
		}
	}
	if audio != 2 {
		t.Fatalf("expected two audio tracks in init map, got %d", audio)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

type hmcMP4Track struct {
	handler string
	header  []byte
}

// This parser handles only small original synthetic MP4 fixtures, never media
// accepted by the application. Reject unsupported box layouts instead of
// guessing offsets and accidentally making a malformed metadata oracle.
func hmcMP4Tracks(t *testing.T, raw []byte) []hmcMP4Track {
	t.Helper()
	type box struct {
		kind    string
		payload []byte
	}
	boxes := func(raw []byte) []box {
		var result []box
		for len(raw) > 0 {
			if len(raw) < 8 {
				t.Fatal("truncated fixture MP4 box")
			}
			size := int(binary.BigEndian.Uint32(raw))
			if size < 8 || size > len(raw) {
				t.Fatal("unsupported fixture MP4 box length")
			}
			result = append(result, box{string(raw[4:8]), raw[8:size]})
			raw = raw[size:]
		}
		return result
	}
	var tracks []hmcMP4Track
	for _, top := range boxes(raw) {
		if top.kind != "moov" {
			continue
		}
		for _, track := range boxes(top.payload) {
			if track.kind != "trak" {
				continue
			}
			var result hmcMP4Track
			for _, part := range boxes(track.payload) {
				if part.kind == "tkhd" {
					result.header = part.payload
				}
				if part.kind == "mdia" {
					for _, media := range boxes(part.payload) {
						if media.kind == "hdlr" && len(media.payload) >= 12 {
							result.handler = string(media.payload[8:12])
						}
					}
				}
			}
			tracks = append(tracks, result)
		}
	}
	return tracks
}

func hmcPreserveFixtureMatrix(t *testing.T, source, initMap string) {
	t.Helper()
	read := func(path string) []byte {
		bytes, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return bytes
	}
	videoMatrices := func(raw []byte) [][]byte {
		var result [][]byte
		for _, track := range hmcMP4Tracks(t, raw) {
			if track.handler != "vide" {
				continue
			}
			offset := 40
			if len(track.header) == 0 || track.header[0] > 1 {
				t.Fatal("unsupported fixture track header version")
			}
			if track.header[0] == 1 {
				offset = 52
			}
			if len(track.header) < offset+36 {
				t.Fatal("truncated fixture track display matrix")
			}
			result = append(result, track.header[offset:offset+36])
		}
		return result
	}
	sourceRaw, initRaw := read(source), read(initMap)
	before, after := videoMatrices(sourceRaw), videoMatrices(initRaw)
	if len(before) != len(after) {
		t.Fatalf("fixture generation changed the video track count: %d to %d", len(before), len(after))
	}
	for i := range before {
		copy(after[i], before[i])
	}
	if err := os.WriteFile(initMap, initRaw, 0600); err != nil {
		t.Fatal(err)
	}
}

func hmcExport(t *testing.T, ctx context.Context, c Config, staged string, offset int64, name, format, mode string) string {
	t.Helper()
	output := filepath.Join(c.DataDir, name+"."+format)
	_, _, err := exportInputs(ctx, c, []mediaInput{{Path: staged, OffsetMS: offset}}, Range{EndMS: 2000}, ExportRequest{Format: format, Quality: "best", CutMode: mode}, output)
	if err != nil {
		t.Fatalf("staged %s export: %v", mode, err)
	}
	return output
}

func TestHLSMediaContractRotationAndSAR(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "hls-sar", 160, 96, "setsar=2")
	for _, kind := range []string{"fmp4", "mpegts"} {
		t.Run(kind+"_sar", func(t *testing.T) {
			staged, offset, input := hmcStage(t, ctx, c, base, kind, kind+"-sar")
			if video := mcVideo(t, input); video.SAR != "2:1" || mcRotation(video) != 0 {
				t.Fatalf("invalid anamorphic HLS INPUT %+v", video)
			}
			video := mcVideo(t, mcProbe(t, ctx, c, staged))
			if video.SAR != "2:1" || video.Width != 160 || video.Height != 96 || mcRotation(video) != 0 {
				t.Fatalf("staging lost anamorphic geometry %+v", video)
			}
			out := hmcExport(t, ctx, c, staged, offset, kind+"-sar-render", "mp4", "accurate")
			mcSquareGeometry(t, ctx, c, out, 320, 96)
			rgb, w, h := mcFrame(t, ctx, c, out, true)
			mcCheckCorners(t, rgb, w, h, [4]int{0, 1, 2, 3})
			copy := hmcExport(t, ctx, c, staged, offset, kind+"-sar-copy", "mp4", "copy")
			copied := mcVideo(t, mcProbe(t, ctx, c, copy))
			if copied.SAR != "2:1" || copied.Width != 160 || copied.Height != 96 {
				t.Fatalf("staged copy lost anamorphic geometry %+v", copied)
			}
		})
	}
	for _, angle := range []int{90, 180} {
		t.Run(fmt.Sprintf("fmp4_rotation_%d", angle), func(t *testing.T) {
			rotated := filepath.Join(c.DataDir, fmt.Sprintf("hls-rotate-%d.mp4", angle))
			mcRotateFixture(t, ctx, c, base, rotated, angle)
			staged, offset, input := hmcStage(t, ctx, c, rotated, "fmp4", fmt.Sprintf("rotated-%d", angle))
			if video := mcVideo(t, input); mcRotation(video) != angle || video.SAR != "2:1" {
				t.Fatalf("invalid rotated fMP4 INPUT %+v", video)
			}
			video := mcVideo(t, mcProbe(t, ctx, c, staged))
			if mcRotation(video) != angle || video.SAR != "2:1" {
				t.Fatalf("staging lost rotation/SAR %+v", video)
			}
			if filepath.Ext(staged) != ".mp4" {
				t.Fatalf("rotated HLS must use matrix-preserving MP4 staging: %s", staged)
			}
			width, height, order := 96, 320, [4]int{1, 3, 0, 2}
			if angle == 180 {
				width, height, order = 320, 96, [4]int{3, 2, 1, 0}
			}
			out := hmcExport(t, ctx, c, staged, offset, fmt.Sprintf("rotated-%d-render", angle), "mp4", "accurate")
			mcSquareGeometry(t, ctx, c, out, width, height)
			rgb, w, h := mcFrame(t, ctx, c, out, true)
			mcCheckCorners(t, rgb, w, h, order)
			copy := hmcExport(t, ctx, c, staged, offset, fmt.Sprintf("rotated-%d-copy", angle), "mp4", "copy")
			copied := mcVideo(t, mcProbe(t, ctx, c, copy))
			if mcRotation(copied) != angle || copied.SAR != "2:1" {
				t.Fatalf("staged copy lost rotation/SAR %+v", copied)
			}
			rgb, w, h = mcFrame(t, ctx, c, copy, false)
			mcCheckCorners(t, rgb, w, h, order)
		})
	}
}

func TestHLSMediaContractDefaultAudio(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "hls-audio-base", 160, 96, "")
	input := filepath.Join(c.DataDir, "hls-multiaudio.mp4")
	mcRun(t, ctx, c, "-i", base, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "2", "-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0", "-c:v", "copy", "-c:a", "aac", "-disposition:a:0", "0", "-disposition:a:1", "default", input)
	staged, offset, streams := hmcStage(t, ctx, c, input, "fmp4", "default-audio", func(dir string) { hmcDefaultSecondAudio(t, dir) })
	var audio []mcStream
	for _, stream := range streams {
		if stream.CodecType == "audio" {
			audio = append(audio, stream)
		}
	}
	if len(audio) != 2 || audio[0].Disposition.Default != 0 || audio[1].Disposition.Default != 1 {
		t.Fatalf("invalid HLS default-audio INPUT %+v", streams)
	}
	mcTone(t, mcPCM(t, ctx, c, filepath.Join(c.DataDir, "default-audio-input.media"), .2, .2, audio[1].Index), 880)
	outputs := mcProbe(t, ctx, c, staged)
	count := 0
	for _, stream := range outputs {
		if stream.CodecType == "audio" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("staging retained %d audio tracks", count)
	}
	mcTone(t, mcPCM(t, ctx, c, staged, .2, .2, mcAudioIndex(t, outputs)), 880)
	for _, variant := range []struct{ format, mode string }{{"mp4", "accurate"}, {"mp4", "copy"}, {"mp3", "accurate"}} {
		out := hmcExport(t, ctx, c, staged, offset, "hls-selected-"+variant.format+"-"+variant.mode, variant.format, variant.mode)
		mcTone(t, mcPCM(t, ctx, c, out, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, c, out))), 880)
	}
}

func TestHLSMediaContractHDRTags(t *testing.T) {
	c, ctx := mcConfig(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "media-contract", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest mcHDRManifest
	if err := json.Unmarshal(raw, &manifest); err != nil || len(manifest.Fixtures) != 2 {
		t.Fatalf("invalid pinned HDR fixture manifest: %v", err)
	}
	for _, fixture := range manifest.Fixtures {
		input := fixture.Name
		path := filepath.Join("testdata", "media-contract", input)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != fixture.SHA256 {
			t.Fatalf("HDR fixture %s hash changed", input)
		}
		for _, kind := range []string{"fmp4", "mpegts"} {
			t.Run(input+"_"+kind, func(t *testing.T) {
				path := filepath.Join("testdata", "media-contract", input)
				staged, offset, streams := hmcStage(t, ctx, c, path, kind, strings.TrimSuffix(input, ".mp4")+"-"+kind)
				before := mcVideo(t, streams)
				if before.Transfer != fixture.Transfer || before.Primaries != "bt2020" || before.Matrix != "bt2020nc" || before.ColorRange != "tv" || before.PixelFormat != "yuv420p10le" {
					t.Fatalf("invalid HDR HLS INPUT %+v", before)
				}
				after := mcVideo(t, mcProbe(t, ctx, c, staged))
				if after.Transfer != before.Transfer || after.Primaries != before.Primaries || after.Matrix != before.Matrix || after.ColorRange != before.ColorRange || after.PixelFormat != before.PixelFormat {
					t.Fatalf("staging lost HDR interpretation: before=%+v after=%+v", before, after)
				}
				originalFrame := mcRun(t, ctx, c, "-i", path, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "yuv420p10le", "-f", "rawvideo", "-")
				stagedFrame := mcRun(t, ctx, c, "-i", staged, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "yuv420p10le", "-f", "rawvideo", "-")
				if string(originalFrame) != string(stagedFrame) {
					t.Fatal("HLS staging changed the actual decoded 10-bit signal")
				}
				out := hmcExport(t, ctx, c, staged, offset, strings.TrimSuffix(input, ".mp4")+"-"+kind+"-sdr", "mp4", "accurate")
				mcCheckSDRGray(t, ctx, c, out)
				copy := hmcExport(t, ctx, c, staged, offset, strings.TrimSuffix(input, ".mp4")+"-"+kind+"-copy", "mp4", "copy")
				copied := mcVideo(t, mcProbe(t, ctx, c, copy))
				if copied.Transfer != before.Transfer || copied.Primaries != before.Primaries || copied.Matrix != before.Matrix || copied.PixelFormat != before.PixelFormat {
					t.Fatalf("staged HDR copy lost metadata %+v", copied)
				}
				copiedFrame := mcRun(t, ctx, c, "-i", copy, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "yuv420p10le", "-f", "rawvideo", "-")
				if string(originalFrame) != string(copiedFrame) {
					t.Fatal("HLS copy changed the decoded 10-bit signal")
				}
			})
		}
	}
}

func hmcJoinPeriods(t *testing.T, root, output string, groups ...string) {
	t.Helper()
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-VERSION:7\n")
	for i, group := range groups {
		raw, err := os.ReadFile(filepath.Join(root, group+"-hls", "index.m3u8"))
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 {
			manifest.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		prefix := group + "-hls/"
		for _, line := range strings.Split(string(raw), "\n") {
			switch {
			case strings.HasPrefix(line, "#EXT-X-MAP:"):
				manifest.WriteString(strings.Replace(line, `URI="`, `URI="`+prefix, 1) + "\n")
			case strings.HasPrefix(line, "#EXTINF:"):
				manifest.WriteString(line + "\n")
			case line != "" && !strings.HasPrefix(line, "#"):
				manifest.WriteString(prefix + line + "\n")
			}
		}
	}
	manifest.WriteString("#EXT-X-ENDLIST\n")
	if err := os.WriteFile(filepath.Join(root, output), []byte(manifest.String()), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestHLSMediaContractContinuityMetadata(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "period-base", 160, 96, "")
	hmcStage(t, ctx, c, base, "fmp4", "period-base")
	rotatedBase := filepath.Join(c.DataDir, "period-rotated-base.mp4")
	mcRotateFixture(t, ctx, c, base, rotatedBase, 90)
	hmcStage(t, ctx, c, rotatedBase, "fmp4", "period-rotated-base")
	for _, change := range []string{"unchanged", "unchanged_rotated", "sar", "rotation", "color", "codec"} {
		t.Run(change, func(t *testing.T) {
			input := base
			firstPeriod := "period-base"
			switch change {
			case "unchanged_rotated":
				input, firstPeriod = rotatedBase, "period-rotated-base"
			case "sar":
				input = mcBaseVideo(t, ctx, c, "period-sar", 160, 96, "setsar=2")
			case "rotation":
				input = filepath.Join(c.DataDir, "period-rotation.mp4")
				mcRotateFixture(t, ctx, c, base, input, 90)
			case "color":
				input = filepath.Join(c.DataDir, "period-color.mp4")
				mcRun(t, ctx, c, "-i", base, "-map", "0", "-vf", "setparams=color_primaries=bt2020:color_trc=bt709:colorspace=bt2020nc", "-c:v", "libx264", "-threads", "1", "-g", "25", "-bf", "0", "-pix_fmt", "yuv420p", "-color_primaries", "bt2020", "-color_trc", "bt709", "-colorspace", "bt2020nc", "-c:a", "copy", input)
			case "codec":
				input = filepath.Join(c.DataDir, "period-codec.mp4")
				mcRun(t, ctx, c, "-i", base, "-map", "0", "-c:v", "mpeg4", "-threads", "1", "-g", "25", "-bf", "0", "-c:a", "copy", input)
			}
			name := "next-" + change
			_, _, streams := hmcStage(t, ctx, c, input, "fmp4", name)
			video := mcVideo(t, streams)
			switch change {
			case "sar":
				if video.SAR != "2:1" {
					t.Fatalf("invalid SAR-changing period INPUT %+v", video)
				}
			case "rotation":
				if mcRotation(video) != 90 {
					t.Fatalf("invalid rotated period INPUT %+v", video)
				}
			case "color":
				if video.Primaries != "bt2020" || video.Matrix != "bt2020nc" {
					t.Fatalf("invalid color-changing period INPUT %+v", video)
				}
			case "codec":
				if video.CodecName != "mpeg4" {
					t.Fatalf("invalid codec-changing period INPUT %+v", video)
				}
			}
			manifest := change + ".m3u8"
			hmcJoinPeriods(t, c.DataDir, manifest, firstPeriod, name)
			guard, _ := fixtureGuard(t, c.DataDir)
			format := platformFormat{URL: "https://media.example/" + manifest, Protocol: "m3u8_native"}
			playlist, err := loadHLS(ctx, guard, format, "best", 0)
			if err != nil {
				t.Fatal(err)
			}
			staging := t.TempDir()
			path, _, err := stageHLS(ctx, c, guard, format, playlist, Range{EndMS: 4000}, staging, 0)
			if strings.HasPrefix(change, "unchanged") {
				if err != nil {
					t.Fatalf("unchanged continuity periods rejected: %v", err)
				}
				video := mcVideo(t, mcProbe(t, ctx, c, path))
				rotation := 0
				if change == "unchanged_rotated" {
					rotation = 90
				}
				if video.SAR != "1:1" || video.Primaries != "bt709" || mcRotation(video) != rotation {
					t.Fatalf("joined unchanged periods lost metadata %+v", video)
				}
				if change == "unchanged_rotated" {
					if filepath.Ext(path) != ".mp4" {
						t.Fatal("joined rotated periods must retain MP4 container")
					}
					rgb, w, h := mcFrame(t, ctx, c, path, false)
					mcCheckCorners(t, rgb, w, h, [4]int{1, 3, 0, 2})
				}
			} else {
				if !errors.Is(err, errUnsupportedStream) {
					t.Fatalf("changed %s periods accepted or wrong failure: %v", change, err)
				}
				entries, readErr := os.ReadDir(staging)
				if readErr != nil || len(entries) != 0 {
					t.Fatalf("changed periods retained failed staging: %v %v", entries, readErr)
				}
			}
		})
	}
}

func TestHLSMediaContractFingerprintUsesSelectedTracks(t *testing.T) {
	c, ctx := mcConfig(t)
	input := mcBaseVideo(t, ctx, c, "fingerprint-base", 160, 96, "")
	info, _, err := probe(ctx, c, input, false)
	if err != nil || info.selectedVideo() == nil || info.selectedAudio() == nil {
		t.Fatalf("invalid actual fingerprint fixture: %v", err)
	}
	clone := func() probeInfo {
		other := info
		other.Streams = append([]probeStream(nil), info.Streams...)
		return other
	}
	renumbered := clone()
	for i := range renumbered.Streams {
		renumbered.Streams[i].Index += 10
		renumbered.Streams[i].Disposition.Default = 0
	}
	if !sameHLSCodecs(info, renumbered) {
		t.Fatal("remuxed stream indices/disposition changed the selected-track fingerprint")
	}
	unselected := clone()
	extra := *info.selectedVideo()
	extra.Index = 20
	extra.Width, extra.Height, extra.SampleAspectRatio = 722, 160, "2:1"
	extra.CodecName, extra.ColorTransfer = "mpeg4", "smpte2084"
	unselected.Streams = append(unselected.Streams, extra)
	if !sameHLSCodecs(info, unselected) {
		t.Fatal("unselected high-index video changed the selected-track fingerprint")
	}
	// Keep codec extradata identical: container color/SAR changes must still
	// reject continuity even when an unchanged SPS alone would match.
	for _, property := range []string{"sar", "color", "codec"} {
		t.Run(property, func(t *testing.T) {
			other := clone()
			video := other.selectedVideo()
			switch property {
			case "sar":
				video.SampleAspectRatio = "2:1"
			case "color":
				video.ColorTransfer = "smpte2084"
			case "codec":
				video.CodecName = "mpeg4"
			}
			if sameHLSCodecs(info, other) {
				t.Fatalf("selected %s change accepted with identical extradata", property)
			}
		})
	}
}
