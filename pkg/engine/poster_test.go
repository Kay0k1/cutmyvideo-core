package engine

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestPosterEngineVerifiedOutputTimestamp(t *testing.T) {
	input, audio := renderFixture(t)
	changing := filepath.Join(t.TempDir(), "changing.mp4")
	renderTool(t, "-v", "error", "-i", input, "-vf", "drawbox=color=blue:t=fill:enable='gte(t,2)'", "-c:v", "libx264", "-threads", "1", "-c:a", "copy", changing)
	// Source changes red→blue at2s. The exported1..3s clip changes at1s and
	// has its own canvas; this proves both the finished input and output clock.
	e := New(Config{FFmpegThreads: 1, MaxOutputBytes: 16 << 20})
	finished := filepath.Join(t.TempDir(), "finished.mp4")
	_, err := e.ExportWithOptions(context.Background(), changing, finished, Range{StartMS: 1000, EndMS: 3000}, AdvancedOptions{Options: Options{Format: "mp4", Quality: "best"}, Canvas: &Canvas{Width: 180, Height: 320, Fit: "contain", Background: "#123456"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"jpeg", "png"} {
		ext := ".jpg"
		if format == "png" {
			ext = ".png"
		}
		output := filepath.Join(t.TempDir(), "poster"+ext)
		r, err := e.GeneratePoster(context.Background(), finished, output, PosterOptions{TimestampMS: 1200, Format: format})
		if err != nil {
			t.Fatal(err)
		}
		if r.Width != 180 || r.Height != 320 || r.TimestampMS != 1200 || r.SizeBytes <= 0 || r.SizeBytes > 16<<20 {
			t.Fatalf("poster metadata %+v", r)
		}
		f, err := os.Open(output)
		if err != nil {
			t.Fatal(err)
		}
		img, actual, err := image.Decode(f)
		f.Close()
		if err != nil || actual != format {
			t.Fatal("independent image decode", actual, err)
		}
		red, green, blue, _ := img.At(5, 5).RGBA()
		if absInt(int(red>>8)-18) > 15 || absInt(int(green>>8)-52) > 15 || absInt(int(blue>>8)-86) > 15 {
			t.Fatal("poster did not preserve completed canvas", red>>8, green>>8, blue>>8)
		}
		red, _, blue, _ = img.At(90, 160).RGBA()
		if blue>>8 < 200 || red>>8 > 40 {
			t.Fatal("poster did not select requested output-clock blue frame")
		}
		before, _ := os.ReadFile(output)
		if _, err = e.GeneratePoster(context.Background(), finished, output, PosterOptions{TimestampMS: 0, Format: format}); !errors.Is(err, ErrOutputExists) {
			t.Fatal("existing output replaced", err)
		}
		after, _ := os.ReadFile(output)
		if string(before) != string(after) {
			t.Fatal("existing image bytes changed")
		}
	}
	early := filepath.Join(t.TempDir(), "early.png")
	if _, err = e.GeneratePoster(context.Background(), finished, early, PosterOptions{TimestampMS: 200, Format: "png"}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(early)
	if err != nil {
		t.Fatal(err)
	}
	img, _, decodeErr := image.Decode(f)
	f.Close()
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	red, _, blue, _ := img.At(90, 160).RGBA()
	if red>>8 < 200 || blue>>8 > 40 {
		t.Fatal("earlier requested frame was not red")
	}
	for _, candidate := range []struct {
		input string
		at    int64
	}{{finished, 1999}, {audio, 0}} {
		missing := filepath.Join(t.TempDir(), "missing.jpg")
		if _, err = e.GeneratePoster(context.Background(), candidate.input, missing, PosterOptions{TimestampMS: candidate.at}); CodeOf(err) != CodeInvalidArgument {
			t.Fatal("missing video frame was not an explicit invalid request", err)
		}
		if _, err = os.Stat(missing); !os.IsNotExist(err) {
			t.Fatal("missing frame published partial output")
		}
	}
	for _, o := range []PosterOptions{{TimestampMS: -1}, {TimestampMS: 2000}, {TimestampMS: 999999}, {Format: "svg"}} {
		output := filepath.Join(t.TempDir(), "bad.jpg")
		if _, err = e.GeneratePoster(context.Background(), finished, output, o); err == nil {
			t.Fatal("invalid poster accepted", o)
		}
		if _, err = os.Stat(output); !os.IsNotExist(err) {
			t.Fatal("invalid poster published output")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(t.TempDir(), "cancel.jpg")
	if _, err = e.GeneratePoster(ctx, finished, output, PosterOptions{}); err == nil {
		t.Fatal("cancelled poster accepted")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("cancelled poster published output")
	}
	limited := New(Config{MaxOutputBytes: 32, FFmpegThreads: 1})
	output = filepath.Join(t.TempDir(), "limited.png")
	if _, err = limited.GeneratePoster(context.Background(), finished, output, PosterOptions{Format: "png"}); err == nil {
		t.Fatal("encoded byte limit ignored")
	}
	if _, err = os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("oversized poster published output")
	}
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
