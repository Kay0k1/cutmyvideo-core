package app

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests deliberately inspect fixtures independently of the application's
// probe and decode real outputs. A matching command line is not a media oracle.
type mcStream struct {
	Index       int    `json:"index"`
	CodecType   string `json:"codec_type"`
	CodecName   string `json:"codec_name"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SAR         string `json:"sample_aspect_ratio"`
	DAR         string `json:"display_aspect_ratio"`
	PixelFormat string `json:"pix_fmt"`
	Transfer    string `json:"color_transfer"`
	Primaries   string `json:"color_primaries"`
	Matrix      string `json:"color_space"`
	ColorRange  string `json:"color_range"`
	Disposition struct {
		Default     int `json:"default"`
		AttachedPic int `json:"attached_pic"`
	} `json:"disposition"`
	SideData []struct {
		Rotation int `json:"rotation"`
	} `json:"side_data_list"`
}

func mcConfig(t *testing.T) (Config, context.Context) {
	t.Helper()
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("mandatory media contract dependency %s: %v", tool, err)
		}
	}
	c := Config{FFmpeg: "ffmpeg", FFprobe: "ffprobe", FFmpegThreads: 1, MaxSourceBytes: 20 << 20, MaxOutputBytes: 20 << 20, MaxRanges: 12, MaxRangeMS: 600000, MaxJobMS: 3600000, DataDir: t.TempDir()}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	return c, ctx
}

func mcRun(t *testing.T, ctx context.Context, c Config, args ...string) []byte {
	t.Helper()
	raw, err := runCommand(ctx, c.FFmpeg, append([]string{"-v", "error", "-nostdin", "-y", "-filter_threads", "1", "-filter_complex_threads", "1"}, args...)...)
	if err != nil {
		t.Fatalf("fixture/decode command failed: %v; args=%q", err, args)
	}
	return raw
}

func mcProbe(t *testing.T, ctx context.Context, c Config, path string) []mcStream {
	t.Helper()
	raw, err := runCommand(ctx, c.FFprobe, "-v", "error", "-show_streams", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Streams []mcStream `json:"streams"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Streams) == 0 {
		t.Fatal("fixture/result has no streams")
	}
	return result.Streams
}

func mcVideo(t *testing.T, streams []mcStream) mcStream {
	t.Helper()
	for _, s := range streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic == 0 {
			return s
		}
	}
	t.Fatal("missing playable video")
	return mcStream{}
}

func mcRotation(s mcStream) int {
	for _, side := range s.SideData {
		if side.Rotation != 0 {
			return (side.Rotation%360 + 360) % 360
		}
	}
	return 0
}

func mcRotateFixture(t *testing.T, ctx context.Context, c Config, base, path string, angle int) {
	t.Helper()
	// FFmpeg 5.x writes a display matrix from this legacy metadata option;
	// newer FFmpeg ignores it. Inspect the actual matrix before using fallback.
	mcRun(t, ctx, c, "-i", base, "-map", "0", "-c", "copy", "-metadata:s:v:0", fmt.Sprintf("rotate=%d", angle), path)
	if mcRotation(mcVideo(t, mcProbe(t, ctx, c, path))) == angle {
		return
	}
	mcRun(t, ctx, c, "-display_rotation", strconv.Itoa(angle), "-i", base, "-map", "0", "-c", "copy", path)
	if mcRotation(mcVideo(t, mcProbe(t, ctx, c, path))) != angle {
		t.Fatal("fixture tools did not write requested display matrix")
	}
}

func mcCorners(t *testing.T, path string, width, height int) {
	t.Helper()
	raw := make([]byte, width*height*3)
	colors := [4][3]byte{{240, 20, 20}, {20, 240, 20}, {20, 20, 240}, {240, 240, 240}}
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			color := colors[(y/(height/2))*2+x/(width/2)]
			copy(raw[(y*width+x)*3:], color[:])
		}
	}
	ppm := append([]byte(fmt.Sprintf("P6\n%d %d\n255\n", width, height)), raw...)
	if err := os.WriteFile(path, ppm, 0600); err != nil {
		t.Fatal(err)
	}
}

func mcBaseVideo(t *testing.T, ctx context.Context, c Config, name string, width, height int, filter string) string {
	t.Helper()
	image := filepath.Join(c.DataDir, name+".ppm")
	mcCorners(t, image, width, height)
	out := filepath.Join(c.DataDir, name+".mp4")
	args := []string{"-loop", "1", "-i", image, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "2", "-r", "25", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-g", "25", "-bf", "0", "-x264-params", "colorprim=bt709:transfer=bt709:colormatrix=bt709", "-c:a", "aac", "-b:a", "96k", "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}
	if filter == "" {
		filter = "setsar=1"
	}
	args = append(args, "-vf", filter)
	mcRun(t, ctx, c, append(args, out)...)
	return out
}

func mcFrame(t *testing.T, ctx context.Context, c Config, path string, noRotate bool) ([]byte, int, int) {
	t.Helper()
	s := mcVideo(t, mcProbe(t, ctx, c, path))
	width, height := s.Width, s.Height
	if !noRotate && (mcRotation(s) == 90 || mcRotation(s) == 270) {
		width, height = height, width
	}
	args := []string{}
	if noRotate {
		args = append(args, "-noautorotate")
	}
	args = append(args, "-i", path, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
	raw := mcRun(t, ctx, c, args...)
	if len(raw) != width*height*3 {
		t.Fatalf("decoded geometry %dx%d has %d RGB bytes", width, height, len(raw))
	}
	return raw, width, height
}

func mcCheckCorners(t *testing.T, raw []byte, w, h int, order [4]int) {
	t.Helper()
	colors := [4][3]byte{{240, 20, 20}, {20, 240, 20}, {20, 20, 240}, {240, 240, 240}}
	for i, point := range [4][2]int{{w / 4, h / 4}, {3 * w / 4, h / 4}, {w / 4, 3 * h / 4}, {3 * w / 4, 3 * h / 4}} {
		got := raw[(point[1]*w+point[0])*3:][:3]
		want := colors[order[i]]
		for channel := range 3 {
			if math.Abs(float64(int(got[channel])-int(want[channel]))) > 35 {
				t.Fatalf("corner %d RGB=%v want=%v, decoded=%dx%d", i, got, want, w, h)
			}
		}
	}
}

func mcExport(t *testing.T, ctx context.Context, c Config, input, name, format, mode string) string {
	t.Helper()
	out := filepath.Join(c.DataDir, name+"."+format)
	start, end, err := exportMedia(ctx, c, []string{input}, false, Range{StartMS: 0, EndMS: 2000}, ExportRequest{Format: format, Quality: "best", CutMode: mode}, out)
	if err != nil {
		t.Fatalf("%s actual export: %v", name, err)
	}
	expectedStart := int64(0)
	if mode == "copy" {
		// Tiny owned fixtures begin at a keyframe. MKV can offset that first
		// presentation timestamp by AAC encoder delay; verify the real input
		// clock instead of declaring all zero-origin requests zero-origin media.
		frames := mcFramePTS(t, ctx, c, input)
		if len(frames) == 0 {
			t.Fatal("copy fixture has no first video frame")
		}
		expectedStart = int64(math.Round(frames[0] * 1000))
	}
	if start != expectedStart || math.Abs(float64(end-2000)) > 100 {
		t.Fatalf("%s unexpected reported bounds %d-%d; actual input requires start %d", name, start, end, expectedStart)
	}
	return out
}

func mcPreview(t *testing.T, ctx context.Context, c Config, input, name string) string {
	t.Helper()
	out := filepath.Join(c.DataDir, name+".mp4")
	if err := renderPreview(ctx, c, []mediaInput{{Path: input}}, Range{StartMS: 0, EndMS: 2000}, out); err != nil {
		t.Fatalf("%s actual preview: %v", name, err)
	}
	return out
}

func mcSquareGeometry(t *testing.T, ctx context.Context, c Config, path string, w, h int) {
	t.Helper()
	s := mcVideo(t, mcProbe(t, ctx, c, path))
	if s.Width != w || s.Height != h || s.SAR != "1:1" || mcRotation(s) != 0 {
		t.Fatalf("normalized geometry %+v; want %dx%d square pixels and no rotation", s, w, h)
	}
}

func TestMediaContractRotationDisplayGeometry(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "corners", 160, 96, "")
	for _, angle := range []int{90, 180, 270} {
		t.Run(strconv.Itoa(angle), func(t *testing.T) {
			input := filepath.Join(c.DataDir, fmt.Sprintf("rotation-%d.mp4", angle))
			mcRotateFixture(t, ctx, c, base, input, angle)
			s := mcVideo(t, mcProbe(t, ctx, c, input))
			if s.Width != 160 || s.Height != 96 || mcRotation(s) != angle {
				t.Fatalf("invalid rotated INPUT: %+v", s)
			}
			order := [4]int{1, 3, 0, 2}
			w, h := 96, 160
			if angle == 180 {
				order = [4]int{3, 2, 1, 0}
				w, h = 160, 96
			} else if angle == 270 {
				order = [4]int{2, 0, 3, 1}
			}
			raw, rw, rh := mcFrame(t, ctx, c, input, false)
			mcCheckCorners(t, raw, rw, rh, order)
			for _, profile := range []string{"fast", "compact"} {
				t.Run(profile, func(t *testing.T) {
					config := c
					config.FFmpegProfile = profile
					out := mcExport(t, ctx, config, input, fmt.Sprintf("rotate-%d-%s", angle, profile), "mp4", "accurate")
					mcSquareGeometry(t, ctx, c, out, w, h)
					rgb, ow, oh := mcFrame(t, ctx, c, out, true)
					mcCheckCorners(t, rgb, ow, oh, order)
				})
			}
			out := mcExport(t, ctx, c, input, fmt.Sprintf("rotate-%d-copy", angle), "mp4", "copy")
			copyInfo := mcVideo(t, mcProbe(t, ctx, c, out))
			if copyInfo.Width != 160 || copyInfo.Height != 96 || mcRotation(copyInfo) != angle {
				t.Fatalf("copy lost coded dimensions/rotation: %+v", copyInfo)
			}
			rgb, ow, oh := mcFrame(t, ctx, c, out, false)
			mcCheckCorners(t, rgb, ow, oh, order)
			out = mcPreview(t, ctx, c, input, fmt.Sprintf("rotate-%d-preview", angle))
			mcSquareGeometry(t, ctx, c, out, w, h)
			rgb, ow, oh = mcFrame(t, ctx, c, out, true)
			mcCheckCorners(t, rgb, ow, oh, order)
		})
	}
	// Portrait display height exceeds 720 even though coded height does not.
	large := mcBaseVideo(t, ctx, c, "portrait-cap", 800, 450, "")
	rotated := filepath.Join(c.DataDir, "portrait-cap-rotated.mp4")
	mcRotateFixture(t, ctx, c, large, rotated, 90)
	_, _, err := exportMedia(ctx, c, []string{rotated}, false, Range{StartMS: 0, EndMS: 2000}, ExportRequest{Format: "mp4", Quality: "720p", CutMode: "copy"}, filepath.Join(c.DataDir, "portrait-cap-copy.mp4"))
	if exportProblem(err).code != "copy_quality_unsupported" {
		t.Fatalf("copy ignored DISPLAY height above quality cap: %v", err)
	}
	for _, profile := range []string{"fast", "compact"} {
		config := c
		config.FFmpegProfile = profile
		out := filepath.Join(c.DataDir, "portrait-cap-"+profile+".mp4")
		if _, _, err := exportMedia(ctx, config, []string{rotated}, false, Range{EndMS: 2000}, ExportRequest{Format: "mp4", Quality: "720p", CutMode: "accurate"}, out); err != nil {
			t.Fatal(err)
		}
		s := mcVideo(t, mcProbe(t, ctx, c, out))
		if s.Height != 720 || s.Width%2 != 0 || math.Abs(float64(s.Width)/float64(s.Height)-450.0/800) > 0.003 || s.SAR != "1:1" {
			t.Fatalf("portrait cap geometry wrong: %+v", s)
		}
	}
}

func TestMediaContractSampleAspectNormalization(t *testing.T) {
	c, ctx := mcConfig(t)
	input := mcBaseVideo(t, ctx, c, "anamorphic", 720, 576, "setsar=16/15")
	s := mcVideo(t, mcProbe(t, ctx, c, input))
	if s.Width != 720 || s.Height != 576 || s.SAR != "16:15" || s.DAR != "4:3" {
		t.Fatalf("invalid anamorphic INPUT %+v", s)
	}
	for _, profile := range []string{"fast", "compact"} {
		t.Run(profile, func(t *testing.T) {
			config := c
			config.FFmpegProfile = profile
			out := mcExport(t, ctx, config, input, "sar-"+profile, "mp4", "accurate")
			mcSquareGeometry(t, ctx, c, out, 768, 576)
			rgb, w, h := mcFrame(t, ctx, c, out, true)
			mcCheckCorners(t, rgb, w, h, [4]int{0, 1, 2, 3})
		})
	}
	copy := mcExport(t, ctx, c, input, "sar-copy", "mp4", "copy")
	s = mcVideo(t, mcProbe(t, ctx, c, copy))
	if s.Width != 720 || s.Height != 576 || s.SAR != "16:15" || s.DAR != "4:3" {
		t.Fatalf("copy changed anamorphic geometry: %+v", s)
	}
	preview := mcPreview(t, ctx, c, input, "sar-preview")
	mcSquareGeometry(t, ctx, c, preview, 640, 480)
	rgb, w, h := mcFrame(t, ctx, c, preview, true)
	mcCheckCorners(t, rgb, w, h, [4]int{0, 1, 2, 3})
}

func mcPCM(t *testing.T, ctx context.Context, c Config, path string, start, duration float64, index int) []int16 {
	t.Helper()
	raw := mcRun(t, ctx, c, "-ss", fmt.Sprint(start), "-i", path, "-t", fmt.Sprint(duration), "-map", fmt.Sprintf("0:%d", index), "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
	if len(raw) < 160 {
		t.Fatal("decoded audio is unexpectedly empty")
	}
	result := make([]int16, len(raw)/2)
	for i := range result {
		result[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	return result
}

func mcTone(t *testing.T, samples []int16, want float64) {
	t.Helper()
	power := func(frequency float64) float64 {
		var re, im float64
		for n, v := range samples {
			phase := 2 * math.Pi * frequency * float64(n) / 16000
			re += float64(v) * math.Cos(phase)
			im += float64(v) * math.Sin(phase)
		}
		return math.Hypot(re, im)
	}
	got, other := power(want), power(440)
	if want == 440 {
		other = power(880)
	}
	if got < 10000 || got < other*8 {
		t.Fatalf("wrong decoded audio tone: want %.0fHz amplitude %.0f competing %.0f", want, got, other)
	}
}

func mcAudioIndex(t *testing.T, streams []mcStream) int {
	t.Helper()
	for _, s := range streams {
		if s.CodecType == "audio" {
			return s.Index
		}
	}
	t.Fatal("no output audio")
	return -1
}

func TestMediaContractDefaultAudioSelection(t *testing.T) {
	c, ctx := mcConfig(t)
	video := mcBaseVideo(t, ctx, c, "audio-base", 160, 96, "")
	for _, withDefault := range []bool{true, false} {
		t.Run(fmt.Sprint(withDefault), func(t *testing.T) {
			input := filepath.Join(c.DataDir, fmt.Sprintf("multiaudio-%t.mkv", withDefault))
			secondDefault := "0"
			want := 440.0
			if withDefault {
				secondDefault = "default"
				want = 880
			}
			mcRun(t, ctx, c, "-i", video, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-f", "lavfi", "-i", "sine=frequency=880:sample_rate=48000", "-t", "2", "-map", "0:v:0", "-map", "1:a:0", "-map", "2:a:0", "-c:v", "copy", "-c:a", "aac", "-disposition:a:0", "0", "-disposition:a:1", secondDefault, input)
			streams := mcProbe(t, ctx, c, input)
			var audio []mcStream
			for _, s := range streams {
				if s.CodecType == "audio" {
					audio = append(audio, s)
				}
			}
			if len(audio) != 2 || audio[0].Disposition.Default != 0 || audio[1].Disposition.Default != map[bool]int{true: 1, false: 0}[withDefault] {
				t.Fatalf("invalid multi-audio INPUT %+v", streams)
			}
			mcTone(t, mcPCM(t, ctx, c, input, .2, .2, audio[0].Index), 440)
			mcTone(t, mcPCM(t, ctx, c, input, .2, .2, audio[1].Index), 880)
			for _, variant := range []struct{ name, format, mode, profile string }{{"fast", "mp4", "accurate", "fast"}, {"compact", "mp4", "accurate", "compact"}, {"copy", "mp4", "copy", "fast"}, {"mp3", "mp3", "accurate", "fast"}} {
				t.Run(variant.name, func(t *testing.T) {
					config := c
					config.FFmpegProfile = variant.profile
					out := mcExport(t, ctx, config, input, fmt.Sprintf("audio-%t-%s", withDefault, variant.name), variant.format, variant.mode)
					outputs := mcProbe(t, ctx, c, out)
					count := 0
					for _, s := range outputs {
						if s.CodecType == "audio" {
							count++
						}
					}
					if count != 1 {
						t.Fatalf("expected one selected audio track, got %+v", outputs)
					}
					mcTone(t, mcPCM(t, ctx, c, out, .2, .2, mcAudioIndex(t, outputs)), want)
				})
			}
			preview := mcPreview(t, ctx, c, input, fmt.Sprintf("audio-%t-preview", withDefault))
			mcTone(t, mcPCM(t, ctx, c, preview, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, c, preview))), want)
		})
	}
}

func TestMediaContractAttachedPicturesAreNotVideo(t *testing.T) {
	c, ctx := mcConfig(t)
	cover := filepath.Join(c.DataDir, "cover.png")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "color=red:size=160x96", "-frames:v", "1", "-threads", "1", cover)
	input := filepath.Join(c.DataDir, "audio-cover.mp3")
	mcRun(t, ctx, c, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-i", cover, "-t", "2", "-map", "0:a:0", "-map", "1:v:0", "-c:a", "libmp3lame", "-c:v", "copy", "-id3v2_version", "3", "-disposition:v:0", "attached_pic", input)
	streams := mcProbe(t, ctx, c, input)
	attached := 0
	for _, s := range streams {
		if s.CodecType == "video" {
			if s.Disposition.AttachedPic != 1 {
				t.Fatalf("invalid cover INPUT %+v", streams)
			}
			attached++
		}
	}
	if attached != 1 {
		t.Fatalf("cover INPUT has %d attached images", attached)
	}
	out := mcExport(t, ctx, c, input, "cover-audio", "mp3", "accurate")
	outputs := mcProbe(t, ctx, c, out)
	for _, s := range outputs {
		if s.CodecType == "video" {
			t.Fatalf("MP3 audio export retained cover as video %+v", outputs)
		}
	}
	mcTone(t, mcPCM(t, ctx, c, out, .2, .2, mcAudioIndex(t, outputs)), 440)
	for _, mode := range []string{"accurate", "copy"} {
		_, _, err := exportMedia(ctx, c, []string{input}, false, Range{EndMS: 2000}, ExportRequest{Format: "mp4", Quality: "best", CutMode: mode}, filepath.Join(c.DataDir, "cover-"+mode+".mp4"))
		if err == nil {
			t.Errorf("%s invented playable video from an attached picture", mode)
		}
	}
	if err := renderPreview(ctx, c, []mediaInput{{Path: input}}, Range{EndMS: 2000}, filepath.Join(c.DataDir, "cover-preview.mp4")); err == nil {
		t.Error("preview invented playable video from cover")
	}
}

func TestMediaContractCopyQualityUsesSelectedVideo(t *testing.T) {
	c, ctx := mcConfig(t)
	base := mcBaseVideo(t, ctx, c, "selected-low", 160, 96, "")
	input := filepath.Join(c.DataDir, "unselected-high.mp4")
	mcRun(t, ctx, c, "-i", base, "-f", "lavfi", "-i", "color=blue:size=160x722:rate=25", "-t", "2", "-map", "0:v:0", "-map", "1:v:0", "-map", "0:a:0", "-c:v", "libx264", "-threads", "1", "-g", "25", "-bf", "0", "-pix_fmt", "yuv420p", "-c:a", "copy", "-disposition:v:0", "0", "-disposition:v:1", "default", input)
	streams := mcProbe(t, ctx, c, input)
	if len(streams) != 3 || streams[0].Width != 160 || streams[0].Height != 96 || streams[1].Height != 722 || streams[1].Disposition.Default != 1 {
		t.Fatalf("invalid multi-video INPUT %+v", streams)
	}
	out := filepath.Join(c.DataDir, "selected-copy.mp4")
	if _, _, err := exportMedia(ctx, c, []string{input}, false, Range{EndMS: 2000}, ExportRequest{Format: "mp4", Quality: "720p", CutMode: "copy"}, out); err != nil {
		t.Fatalf("unselected video incorrectly blocked selected low-resolution copy: %v", err)
	}
	s := mcVideo(t, mcProbe(t, ctx, c, out))
	if s.Width != 160 || s.Height != 96 {
		t.Fatalf("selected wrong video %+v", s)
	}
	rgb, w, h := mcFrame(t, ctx, c, out, true)
	mcCheckCorners(t, rgb, w, h, [4]int{0, 1, 2, 3})
}

// HDR fixtures contain known absolute-luminance neutral bars, rather than
// ordinary SDR testsrc with HDR tags. The manifest pins the original bytes.
type mcHDRManifest struct {
	Fixtures []struct {
		Name     string  `json:"name"`
		SHA256   string  `json:"sha256"`
		Transfer string  `json:"transfer"`
		Luma10   []int   `json:"luma10"`
		YUV10    [][]int `json:"yuv10"`
	} `json:"fixtures"`
}

func TestMediaContractHDRToneMappingAndCopy(t *testing.T) {
	c, ctx := mcConfig(t)
	raw, err := os.ReadFile(filepath.Join("testdata", "media-contract", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest mcHDRManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Fixtures) != 2 {
		t.Fatal("expected PQ and HLG fixtures")
	}
	levelsBySignal := make(map[string]map[string][]int)
	for _, fixture := range manifest.Fixtures {
		t.Run(fixture.Transfer, func(t *testing.T) {
			input := filepath.Join("testdata", "media-contract", fixture.Name)
			bytes, err := os.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(bytes)
			if hex.EncodeToString(sum[:]) != fixture.SHA256 {
				t.Fatal("HDR fixture hash changed")
			}
			s := mcVideo(t, mcProbe(t, ctx, c, input))
			if s.Width != 128 || s.Height != 80 || s.Transfer != fixture.Transfer || s.Primaries != "bt2020" || s.Matrix != "bt2020nc" || s.PixelFormat != "yuv420p10le" || s.ColorRange != "tv" {
				t.Fatalf("invalid known-signal HDR INPUT %+v", s)
			}
			yuv := mcRun(t, ctx, c, "-i", input, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "yuv420p10le", "-f", "rawvideo", "-")
			if len(yuv) != 128*80*3 {
				t.Fatal("bad decoded 10-bit input frame")
			}
			if len(fixture.YUV10) != 8 || len(fixture.Luma10) != 8 {
				t.Fatal("missing physical signal samples")
			}
			for i, want := range fixture.YUV10 {
				if len(want) != 3 || want[0] != fixture.Luma10[i] {
					t.Fatal("inconsistent physical signal samples")
				}
				offsets := []int{40*128 + i*16 + 8, 128*80 + 20*64 + i*8 + 4, 128*80 + 64*40 + 20*64 + i*8 + 4}
				for plane, offset := range offsets {
					got := int(binary.LittleEndian.Uint16(yuv[offset*2:]))
					if got != want[plane] {
						t.Fatalf("lossless HDR physical signal bar%d plane%d=%d want%d", i, plane, got, want[plane])
					}
				}
			}
			levelsBySignal[fixture.Transfer] = make(map[string][]int)
			for _, profile := range []string{"fast", "compact"} {
				t.Run(profile, func(t *testing.T) {
					config := c
					config.FFmpegProfile = profile
					out := mcExport(t, ctx, config, input, "hdr-"+fixture.Transfer+"-"+profile, "mp4", "accurate")
					levelsBySignal[fixture.Transfer][profile] = mcCheckSDRGray(t, ctx, c, out)
				})
			}
			preview := mcPreview(t, ctx, c, input, "hdr-"+fixture.Transfer+"-preview")
			levelsBySignal[fixture.Transfer]["preview"] = mcCheckSDRGray(t, ctx, c, preview)
			copy := mcExport(t, ctx, c, input, "hdr-"+fixture.Transfer+"-copy", "mp4", "copy")
			s = mcVideo(t, mcProbe(t, ctx, c, copy))
			if s.Transfer != fixture.Transfer || s.Primaries != "bt2020" || s.Matrix != "bt2020nc" || s.PixelFormat != "yuv420p10le" {
				t.Fatalf("compatible HDR copy lost color/bit-depth %+v", s)
			}
			copied := mcRun(t, ctx, c, "-i", copy, "-map", "0:v:0", "-frames:v", "1", "-pix_fmt", "yuv420p10le", "-f", "rawvideo", "-")
			if string(copied) != string(yuv) {
				t.Fatal("HDR copy changed decoded 10-bit frame")
			}
			mp3 := mcExport(t, ctx, c, input, "hdr-"+fixture.Transfer+"-audio", "mp3", "accurate")
			mcTone(t, mcPCM(t, ctx, c, mp3, .2, .2, mcAudioIndex(t, mcProbe(t, ctx, c, mp3))), 440)
		})
	}
	// The two signals encode the same absolute luminances through independent
	// transfer functions. Their SDR interpretation should remain close even
	// with the different declared reference peaks and 8-bit encoder rounding.
	for _, variant := range []string{"fast", "compact", "preview"} {
		pq, hlg := levelsBySignal["smpte2084"][variant], levelsBySignal["arib-std-b67"][variant]
		if len(pq) != 7 || len(hlg) != 7 {
			continue
		} // The failed child already records the actual failure.
		for i := range pq {
			if mcAbs(pq[i]-hlg[i]) > 12 {
				t.Errorf("same-nit PQ/HLG %s bar%d differ: %v vs %v", variant, i, pq, hlg)
			}
		}
	}
	// SDR controls must retain the visible colors and BT.709 interpretation.
	sdr := mcBaseVideo(t, ctx, c, "sdr-control", 160, 96, "")
	for _, profile := range []string{"fast", "compact"} {
		config := c
		config.FFmpegProfile = profile
		out := mcExport(t, ctx, config, sdr, "sdr-"+profile, "mp4", "accurate")
		s := mcVideo(t, mcProbe(t, ctx, c, out))
		if s.Transfer != "bt709" || s.Primaries != "bt709" || s.Matrix != "bt709" {
			t.Fatalf("SDR control lost BT709 %+v", s)
		}
		rgb, w, h := mcFrame(t, ctx, c, out, true)
		mcCheckCorners(t, rgb, w, h, [4]int{0, 1, 2, 3})
	}
}

func mcCheckSDRGray(t *testing.T, ctx context.Context, c Config, path string) []int {
	t.Helper()
	s := mcVideo(t, mcProbe(t, ctx, c, path))
	if s.Transfer != "bt709" || s.Primaries != "bt709" || s.Matrix != "bt709" || s.PixelFormat != "yuv420p" || s.ColorRange != "tv" {
		t.Fatalf("HDR output is not declared SDR BT709 limited-range 8-bit: %+v", s)
	}
	rgb, w, h := mcFrame(t, ctx, c, path, true)
	last := -1
	var levels []int
	for i := range 7 {
		offset := ((h/2)*w + (2*i+1)*w/16) * 3
		pixel := rgb[offset:][:3]
		if mcAbs(int(pixel[0])-int(pixel[1])) > 5 || mcAbs(int(pixel[1])-int(pixel[2])) > 5 {
			t.Fatalf("neutral HDR bar acquired tint: %v", pixel)
		}
		value := int(pixel[0])
		levels = append(levels, value)
		if i == 0 && value > 8 || i > 0 && value <= last {
			t.Fatalf("tone-mapped absolute-luminance bars not monotonic/black: %v", levels)
		}
		last = value
	}
	// At 203 nits, correctly linearized HDR should map to bright SDR; naive
	// 10-bit-to-8-bit conversion leaves PQ midgray near 149. Allow encoder and
	// inferred peak differences while rejecting that demonstrated wrong curve.
	if levels[4] < 190 || levels[4] > 250 || levels[6] < 230 {
		t.Fatalf("known-nit bars have wrong SDR luminance: %v", levels)
	}
	redOffset := ((h/2)*w + 15*w/16) * 3
	red := rgb[redOffset:][:3]
	if int(red[0]) < int(red[1])+40 || int(red[0]) < int(red[2])+40 {
		t.Fatalf("known HDR red color bar lost channel identity: %v", red)
	}
	t.Logf("decoded SDR known-nit bars: %v", levels)
	return levels
}

func mcAbs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func mcFramePTS(t *testing.T, ctx context.Context, c Config, path string) []float64 {
	t.Helper()
	selected := mcVideo(t, mcProbe(t, ctx, c, path))
	raw, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", strconv.Itoa(selected.Index), "-show_frames", "-show_entries", "frame=best_effort_timestamp_time", "-of", "json", path)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Frames []struct {
			PTS string `json:"best_effort_timestamp_time"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	var pts []float64
	for _, f := range result.Frames {
		value, err := strconv.ParseFloat(f.PTS, 64)
		if err != nil {
			t.Fatal(err)
		}
		pts = append(pts, value)
	}
	return pts
}

func TestMediaContractVariableFrameTimestamps(t *testing.T) {
	c, ctx := mcConfig(t)
	colors := [][3]byte{{240, 20, 20}, {20, 240, 20}, {20, 20, 240}, {240, 240, 20}, {20, 240, 240}, {240, 20, 240}, {230, 230, 230}, {70, 70, 70}, {70, 70, 70}}
	t.Run("mono_silence_AAC_bounded", func(t *testing.T) {
		// Keep the exact original signal: moving the pulse to improve the
		// fine-grid test otherwise removes the reproducible AAC regression.
		fixtureRoot := filepath.Join("testdata", "media-contract")
		metadata, err := os.ReadFile(filepath.Join(fixtureRoot, "mono-silence-vfr.json"))
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(metadata, &fixture); err != nil {
			t.Fatal(err)
		}
		input := filepath.Join(fixtureRoot, fixture.Name)
		encoded, err := os.ReadFile(input)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		if fixture.Name != "mono-silence-vfr.mp4" || hex.EncodeToString(sum[:]) != fixture.SHA256 {
			t.Fatal("original mono-silence fixture hash changed")
		}
		video := mcVideo(t, mcProbe(t, ctx, c, input))
		if video.Width != 32 || video.Height != 24 || video.PixelFormat != "yuv420p" {
			t.Fatalf("invalid original mono-silence video %+v", video)
		}
		audio, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", "a", "-show_entries", "stream=channels", "-of", "json", input)
		if err != nil {
			t.Fatal(err)
		}
		var streams struct {
			Streams []struct {
				Channels int `json:"channels"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(audio, &streams); err != nil {
			t.Fatal(err)
		}
		if len(streams.Streams) != 1 || streams.Streams[0].Channels != 1 {
			t.Fatal("original silence regression requires exactly one mono track")
		}
		expected := []float64{0, .04, .20, .32, .60, .80, 1.04, 1.40, 1.96}
		mcCheckPTS(t, mcFramePTS(t, ctx, c, input), expected)
		mcCheckFrameIDs(t, ctx, c, input, colors)
		mcPulseAt(t, ctx, c, input, .600, .700)
		bounded, cancel := context.WithTimeout(ctx, 8*time.Second)
		defer cancel()
		started := time.Now()
		out := mcExport(t, bounded, c, input, "mono-silence-bounded", "mp4", "accurate")
		t.Logf("original mono-silence accurate export completed in %s", time.Since(started))
		mcCheckPTS(t, mcFramePTS(t, ctx, c, out), expected)
		mcCheckFrameIDs(t, ctx, c, out, colors)
		mcPulseAt(t, ctx, c, out, .600, .700)
	})
	durations := []float64{.041, .166, .116, .284, .204, .236, .356, .564, .033}
	var concat strings.Builder
	concat.WriteString("ffconcat version 1.0\n")
	for i, color := range colors {
		name := fmt.Sprintf("vfr-%d.ppm", i)
		raw := make([]byte, 32*24*3)
		for p := 0; p < len(raw); p += 3 {
			copy(raw[p:], color[:])
		}
		if err := os.WriteFile(filepath.Join(c.DataDir, name), append([]byte("P6\n32 24\n255\n"), raw...), 0600); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&concat, "file '%s'\noption framerate 1000\nduration %.3f\n", name, durations[i])
	}
	// The final repeated image carries the intended end timestamp to the demuxer.
	concat.WriteString("file 'vfr-8.ppm'\noption framerate 1000\n")
	list := filepath.Join(c.DataDir, "vfr.ffconcat")
	if err := os.WriteFile(list, []byte(concat.String()), 0600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(c.DataDir, "vfr-input.mp4")
	mcRun(t, ctx, c, "-f", "concat", "-safe", "0", "-i", list, "-f", "lavfi", "-i", "aevalsrc=if(between(t\\,0.607\\,0.707)\\,0.2*sin(2*PI*1000*t)\\,0):s=48000:d=2", "-t", "2", "-fps_mode", "vfr", "-c:v", "libx264", "-threads", "1", "-bf", "0", "-g", "25", "-pix_fmt", "yuv420p", "-c:a", "aac", input)
	expected := []float64{0, .041, .207, .323, .607, .811, 1.047, 1.403, 1.967}
	pts := mcFramePTS(t, ctx, c, input)
	mcCheckPTS(t, pts, expected)
	mcCheckFrameIDs(t, ctx, c, input, colors)
	mcPulse(t, ctx, c, input)
	clock, err := runCommand(ctx, c.FFprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=time_base,r_frame_rate,avg_frame_rate", "-of", "json", input)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("validated fine-grid VFR input PTS=%v clock=%s", pts, strings.TrimSpace(string(clock)))
	for _, profile := range []string{"fast", "compact"} {
		t.Run(profile, func(t *testing.T) {
			config := c
			config.FFmpegProfile = profile
			out := mcExport(t, ctx, config, input, "vfr-"+profile, "mp4", "accurate")
			mcCheckPTS(t, mcFramePTS(t, ctx, c, out), expected)
			mcCheckFrameIDs(t, ctx, c, out, colors)
			mcPulse(t, ctx, c, out)
		})
	}
	copy := mcExport(t, ctx, c, input, "vfr-copy", "mp4", "copy")
	mcCheckPTS(t, mcFramePTS(t, ctx, c, copy), expected)
	mcCheckFrameIDs(t, ctx, c, copy, colors)
	mcPulse(t, ctx, c, copy)
	preview := mcPreview(t, ctx, c, input, "vfr-preview")
	previewPTS := mcFramePTS(t, ctx, c, preview)
	if len(previewPTS) < 45 || len(previewPTS) > 50 {
		t.Fatalf("preview does not provide declared 24fps cadence: %v", previewPTS)
	}
	for i := 1; i < len(previewPTS); i++ {
		if math.Abs(previewPTS[i]-previewPTS[i-1]-1.0/24) > .001 {
			t.Fatalf("preview frame cadence is irregular: %v", previewPTS)
		}
	}
	mcPulse(t, ctx, c, preview)
	// Preview intentionally resamples cadence. Check frame identity at times
	// safely inside the corresponding irregular source-frame windows.
	for _, sample := range []struct {
		at float64
		id int
	}{{.25, 2}, {.46, 3}, {.73, 4}, {.95, 5}, {1.20, 6}, {1.65, 7}} {
		raw := mcRun(t, ctx, c, "-ss", fmt.Sprint(sample.at), "-i", preview, "-vf", "scale=1:1", "-frames:v", "1", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
		if len(raw) != 3 {
			t.Fatal("missing preview frame")
		}
		for channel := range 3 {
			if mcAbs(int(raw[channel])-int(colors[sample.id][channel])) > 25 {
				t.Fatalf("preview lost VFR identity at %.2fs: %v want %v", sample.at, raw, colors[sample.id])
			}
		}
	}
}

func mcCheckPTS(t *testing.T, got, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("accurate/copy changed VFR frame count: pts=%v want=%v", got, want)
	}
	for i := range want {
		if math.Abs(got[i]-want[i]) > .002 {
			t.Fatalf("VFR presentation timestamp %d %.6f want%.6f", i, got[i], want[i])
		}
	}
}

func mcCheckFrameIDs(t *testing.T, ctx context.Context, c Config, path string, colors [][3]byte) {
	t.Helper()
	rgb := mcRun(t, ctx, c, "-i", path, "-vf", "scale=1:1", "-fps_mode", "passthrough", "-pix_fmt", "rgb24", "-f", "rawvideo", "-")
	if len(rgb) != len(colors)*3 {
		t.Fatalf("VFR decoded identity count %d want%d", len(rgb)/3, len(colors))
	}
	for i, color := range colors {
		for ch := range 3 {
			if mcAbs(int(rgb[i*3+ch])-int(color[ch])) > 25 {
				t.Fatalf("VFR frame%d identity=%v want=%v", i, rgb[i*3:][:3], color)
			}
		}
	}
}

func mcPulse(t *testing.T, ctx context.Context, c Config, path string) {
	t.Helper()
	mcPulseAt(t, ctx, c, path, .607, .707)
}

func mcPulseAt(t *testing.T, ctx context.Context, c Config, path string, expectedStart, expectedEnd float64) {
	t.Helper()
	// Decode from the beginning, so AAC priming/edit-list handling is part of
	// the observed timeline. A zero input seek can discard its leading packet.
	raw := mcRun(t, ctx, c, "-i", path, "-t", "1", "-map", fmt.Sprintf("0:%d", mcAudioIndex(t, mcProbe(t, ctx, c, path))), "-vn", "-ac", "1", "-ar", "16000", "-f", "s16le", "-")
	if len(raw) < 160 {
		t.Fatal("decoded pulse audio is unexpectedly empty")
	}
	pcm := make([]int16, len(raw)/2)
	for i := range pcm {
		pcm[i] = int16(binary.LittleEndian.Uint16(raw[i*2:]))
	}
	var start, end int
	for i, value := range pcm {
		if mcAbs(int(value)) > 500 {
			if start == 0 {
				start = i
			}
			end = i
		}
	}
	if math.Abs(float64(start)/16000-expectedStart) > .012 || math.Abs(float64(end)/16000-expectedEnd) > .012 {
		t.Fatalf("VFR audio pulse moved relative to expected %.3f–%.3fs: %.4f–%.4f", expectedStart, expectedEnd, float64(start)/16000, float64(end)/16000)
	}
}
