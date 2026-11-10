package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// syncFixture writes mono 48 kHz PCM with irregular noise and tone attacks
// from the seed, delayed by the given number of samples.
func syncFixture(t *testing.T, name string, seed int64, seconds float64, delay int) string {
	t.Helper()
	const rate = 48000
	r := rand.New(rand.NewSource(seed))
	samples := make([]float64, int(seconds*rate)+delay)
	for at := 0.1; at < seconds-0.4; at += 0.1 + 0.35*r.Float64()*r.Float64() {
		pitch, start := 150+800*r.Float64(), int(at*rate)+delay
		for i := 0; i < rate/8; i++ {
			x := float64(i) / rate
			samples[start+i] += 0.3 * math.Min(1, x/0.005) * math.Exp(-x*30) * (math.Sin(2*math.Pi*pitch*x) + 0.3*(r.Float64()*2-1))
		}
	}
	data := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(int16(math.Max(-1, math.Min(1, v))*30000)))
	}
	header := append([]byte("RIFF"), binary.LittleEndian.AppendUint32(nil, uint32(36+len(data)))...)
	header = append(header, "WAVEfmt "...)
	header = binary.LittleEndian.AppendUint32(header, 16)
	header = binary.LittleEndian.AppendUint16(header, 1)
	header = binary.LittleEndian.AppendUint16(header, 1)
	header = binary.LittleEndian.AppendUint32(header, rate)
	header = binary.LittleEndian.AppendUint32(header, rate*2)
	header = binary.LittleEndian.AppendUint16(header, 2)
	header = binary.LittleEndian.AppendUint16(header, 16)
	header = append(header, "data"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(len(data)))
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSyncCLIReportsOffsetsAndExitCodes(t *testing.T) {
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skip(name + " is not installed")
		}
	}
	reference := syncFixture(t, "-track.wav", 1, 20, 0)
	later := syncFixture(t, "-clip.wav", 1, 20, 72000)
	other := syncFixture(t, "-other.wav", 2, 20, 0)

	var stdout, stderr bytes.Buffer
	if code := runCLI([]string{"--json-errors", "sync", "--ref", reference, "--input", later}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("exit %d, stderr %s", code, &stderr)
	}
	var result struct {
		Version int
		Inputs  []struct {
			Status     string
			Placements []struct {
				OffsetMS float64 `json:"offset_ms"`
				Method   string
			}
		}
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Version != 1 || len(result.Inputs) != 1 || result.Inputs[0].Status != "match" || len(result.Inputs[0].Placements) != 1 || math.Abs(result.Inputs[0].Placements[0].OffsetMS-1500) > 2 {
		t.Fatalf("result %s: %v", &stdout, err)
	}

	// The report is printed even when an input does not match; the exit code
	// and the JSON error carry the refusal.
	stdout.Reset()
	if code := runCLI([]string{"--json-errors", "sync", "--ref", reference, "--input", later, "--input", other}, &stdout, &stderr); code != 1 {
		t.Fatalf("exit %d", code)
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || len(result.Inputs) != 2 || result.Inputs[0].Status != "match" || result.Inputs[1].Status != "no_match" {
		t.Fatalf("partial result %s: %v", &stdout, err)
	}
	if !strings.Contains(stderr.String(), `"code":"no_match"`) {
		t.Fatalf("stderr %s", &stderr)
	}

	stdout.Reset()
	stderr.Reset()
	if code := runCLI([]string{"--json-errors", "analyze", "repeats", "--input", reference}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"repeats":[]`) {
		t.Fatalf("repeats: exit %d, %s %s", code, &stdout, &stderr)
	}
}

func TestSyncCLIRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{
		{"sync", "--input", "clip.mp4"},
		{"sync", "--ref", "track.wav"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "extra"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "--threads", "0"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "--timeout", "0"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "--ref-start", "5", "--ref-end", "5"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "--start", "9", "--end", "3"},
		{"sync", "--ref", "track.wav", "--input", "clip.mp4", "--band", "bass"},
		{"analyze", "repeats", "--input", "track.wav", "--audio-track", "1"},
	} {
		var stdout, stderr bytes.Buffer
		if code := runCLI(append([]string{"--json-errors"}, args...), &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), `"code":"invalid_argument"`) {
			t.Errorf("%v: exit %d, stdout %s, stderr %s", args, code, &stdout, &stderr)
		}
	}
	var stdout, stderr bytes.Buffer
	if err := runArgs([]string{"sync", "--help"}, &stdout, &stderr); err != nil || !strings.Contains(stdout.String(), "input_ms = ref_ms + offset_ms") {
		t.Fatalf("help: %v %s", err, &stdout)
	}
}
