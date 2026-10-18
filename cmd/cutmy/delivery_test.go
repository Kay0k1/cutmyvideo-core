package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"image"
	_ "image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/pkg/engine"
)

func TestDeliveryFlagsExactRational(t *testing.T) {
	for text, want := range map[string]engine.Speed{"0.5": {Numerator: 1, Denominator: 2}, "1": {Numerator: 1, Denominator: 1}, "1.250": {Numerator: 5, Denominator: 4}, "2.000000000000000": {Numerator: 2, Denominator: 1}} {
		got, err := parseSpeed(text)
		if err != nil || *got != want {
			t.Fatalf("decimal speed %q: %+v %v", text, got, err)
		}
	}
	for _, text := range []string{"", "-1", "+1", "1e0", "NaN", "Inf", "1/2", "0.499", "2.001", "1.0001", ".5", "1.", " 1"} {
		if _, err := parseSpeed(text); err == nil {
			t.Fatal("accepted malformed or unrepresentable speed", text)
		}
	}
	f := flag.NewFlagSet("clip", flag.ContinueOnError)
	r := registerRenderFlags(f)
	if err := f.Parse([]string{"--speed", ""}); err != nil {
		t.Fatal(err)
	}
	if err := r.apply(f, &engine.AdvancedOptions{}); err == nil {
		t.Fatal("accepted explicit empty speed")
	}
	for _, value := range []string{"0", "-1"} {
		f := flag.NewFlagSet("clip", flag.ContinueOnError)
		r := registerRenderFlags(f)
		if err := f.Parse([]string{"--target-size-bytes", value}); err != nil {
			t.Fatal(err)
		}
		if err := r.apply(f, &engine.AdvancedOptions{}); err == nil {
			t.Fatal("accepted nonpositive explicit byte target")
		}
	}
}

func TestPosterCLIValidation(t *testing.T) {
	for _, args := range [][]string{{"poster"}, {"poster", "--input", "missing", "--output", "poster.png", "--at", "NaN"}, {"poster", "--input", "missing", "--output", "poster.png", "--at", "0", "--format", "svg"}, {"poster", "--input", "missing", "--output", "poster.png", "--at", "0", "--threads", "0"}} {
		var stdout, stderr bytes.Buffer
		if err := runArgs(args, &stdout, &stderr); err == nil {
			t.Fatal("accepted incomplete/invalid poster flags", args)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"poster", "--help"}, &stdout, &stderr); err != nil || stdout.Len()+stderr.Len() == 0 {
		t.Fatal("poster help unavailable", err)
	}
}

func TestDeliveryCLIWorkflow(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("required CLI delivery dependency %s: %v", tool, err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	directory := t.TempDir()
	input := filepath.Join(directory, "source.mp4")
	fixture := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=red:size=160x96:rate=25:duration=2", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=2", "-c:v", "libx264", "-threads", "1", "-pix_fmt", "yuv420p", "-c:a", "aac", input)
	if raw, err := fixture.CombinedOutput(); err != nil {
		t.Fatalf("delivery fixture: %v %s", err, raw)
	}
	var completed string
	for _, format := range []string{"wav", "flac", "m4a", "mp4"} {
		output := filepath.Join(directory, "result."+format)
		args := []string{"clip", "--input", input, "--end", "2", "--output", output, "--threads", "1"}
		if format == "m4a" {
			args = append(args, "--mode", "copy")
		}
		if format == "mp4" {
			args = append(args, "--speed", "1.25", "--target-size-bytes", "262144")
			completed = output
		}
		var stdout, stderr bytes.Buffer
		if err := runArgs(args, &stdout, &stderr); err != nil {
			t.Fatalf("CLI %s: %v %s", format, err, &stderr)
		}
		var result engine.AdvancedResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Path != output || result.SizeBytes <= 0 {
			t.Fatal("missing CLI published output")
		}
		raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type,codec_name", "-of", "json", output).Output()
		if err != nil {
			t.Fatal(err)
		}
		var info struct {
			Format struct {
				Duration string `json:"duration"`
			} `json:"format"`
			Streams []struct {
				Type  string `json:"codec_type"`
				Codec string `json:"codec_name"`
			} `json:"streams"`
		}
		if err = json.Unmarshal(raw, &info); err != nil {
			t.Fatal(err)
		}
		duration, err := strconv.ParseFloat(info.Format.Duration, 64)
		if err != nil {
			t.Fatal(err)
		}
		want := 2.0
		if format == "mp4" {
			want = 1.6
			if result.SizeBytes > 262144 || len(result.Segments) != 1 || result.Segments[0].OutputEndMS != 1600 {
				t.Fatal("CLI dropped speed/size controls")
			}
		} else {
			codec := map[string]string{"wav": "pcm_s24le", "flac": "flac", "m4a": "aac"}[format]
			if len(info.Streams) != 1 || info.Streams[0].Type != "audio" || info.Streams[0].Codec != codec {
				t.Fatal("CLI selected wrong audio container/codec")
			}
		}
		if math.Abs(duration-want) > .15 {
			t.Fatalf("CLI duration %s: %v instead of %v", format, duration, want)
		}
		if raw, err = exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", output, "-f", "null", "-").CombinedOutput(); err != nil {
			t.Fatalf("CLI output did not fully decode: %v %s", err, raw)
		}
	}
	poster := filepath.Join(directory, "poster.png")
	args := []string{"poster", "--input", completed, "--output", poster, "--at", "0.500", "--format", "png", "--threads", "1"}
	var stdout, stderr bytes.Buffer
	if err := runArgs(args, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(poster)
	if err != nil {
		t.Fatal(err)
	}
	im, format, err := image.Decode(bytes.NewReader(data))
	if err != nil || format != "png" || im.Bounds().Dx() != 160 || im.Bounds().Dy() != 96 {
		t.Fatal("CLI poster dimensions/decode differ", err)
	}
	r, g, b, _ := im.At(80, 48).RGBA()
	if r < 45000 || g > 15000 || b > 15000 {
		t.Fatal("CLI poster contains wrong picture")
	}
	if err := runArgs(args, &stdout, &stderr); err == nil {
		t.Fatal("poster overwrote existing image")
	}
	again, err := os.ReadFile(poster)
	if err != nil || !bytes.Equal(data, again) {
		t.Fatal("poster collision changed existing image")
	}
}
