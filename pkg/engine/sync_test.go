package engine

import (
	"context"
	"encoding/binary"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// syncSong is a deterministic recording with irregular attacks: tone bursts of
// varying pitch and length plus short noise hits. The oracle is the sample
// position at which each fixture places it, not another detector.
func syncSong(seconds float64, seed int64) []float64 {
	const rate = 48000
	r := rand.New(rand.NewSource(seed))
	out := make([]float64, int(seconds*rate))
	for t := 0.2 * r.Float64(); t < seconds-0.5; t += 0.09 + 0.4*r.Float64()*r.Float64() {
		at := int(t * rate)
		if r.Float64() < 0.4 {
			previous := 0.0
			for i := 0; i < rate/25 && at+i < len(out); i++ {
				noise := r.Float64()*2 - 1
				out[at+i] += 0.3 * (noise - 0.5*previous) * math.Exp(-float64(i)/rate*90)
				previous = noise
			}
			continue
		}
		pitch, length := 120+900*r.Float64(), 0.08+0.25*r.Float64()
		for i := 0; i < int(length*rate) && at+i < len(out); i++ {
			x := float64(i) / rate
			out[at+i] += 0.25 * math.Min(1, x/0.006) * math.Exp(-x*5/length) * (math.Sin(2*math.Pi*pitch*x) + 0.5*math.Sin(4*math.Pi*pitch*x))
		}
	}
	return out
}

// syncWAV writes 48 kHz mono PCM made of the given parts at sample offsets.
func syncWAV(t *testing.T, seconds float64, parts map[int][]float64) string {
	t.Helper()
	const rate = 48000
	samples := make([]float64, int(seconds*rate))
	for at, part := range parts {
		for i, v := range part {
			if at+i >= 0 && at+i < len(samples) {
				samples[at+i] += v
			}
		}
	}
	data := make([]byte, 2*len(samples))
	for i, v := range samples {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(int16(math.Max(-1, math.Min(1, v))*30000)))
	}
	var header []byte
	header = append(header, "RIFF"...)
	header = binary.LittleEndian.AppendUint32(header, uint32(36+len(data)))
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
	path := filepath.Join(t.TempDir(), "-sync.wav")
	if err := os.WriteFile(path, append(header, data...), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func syncEngine() *Engine {
	return New(Config{FFmpegThreads: 2, InspectTimeout: time.Minute, ExportTimeout: time.Minute})
}

func TestSyncAudioPlacesReferenceOnOriginalTimelines(t *testing.T) {
	analysisTools(t)
	song := syncSong(40, 1)
	reference := syncWAV(t, 40, map[int][]float64{0: song})
	// The same recording starts 3.38 s into the input, after other material.
	input := syncWAV(t, 50, map[int][]float64{162240: song, 0: syncSong(3, 2)})
	result, err := syncEngine().SyncAudio(context.Background(), reference, []string{input}, SyncOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Version != 1 || len(result.Inputs) != 1 || result.Inputs[0].Status != "match" || len(result.Inputs[0].Placements) != 1 {
		t.Fatalf("result: %+v", result)
	}
	p := result.Inputs[0].Placements[0]
	if math.Abs(p.OffsetMS-3380) > 2 || p.Speed != 1 || p.Confidence < 0.9 || p.Method != "waveform" {
		t.Fatalf("placement: %+v", p)
	}
	if p.RefStartMS > 1000 || p.RefEndMS < 39000 || math.Abs(float64(p.InputStartMS-p.RefStartMS)-3380) > 2 {
		t.Fatalf("matched range: %+v", p)
	}

	// Windows keep offsets on the original timelines.
	result, err = syncEngine().SyncAudio(context.Background(), reference, []string{input}, SyncOptions{RefStartMS: 10000, RefEndMS: 30000, StartMS: 5000})
	if err != nil {
		t.Fatal(err)
	}
	p = result.Inputs[0].Placements[0]
	if math.Abs(p.OffsetMS-3380) > 2 || p.RefStartMS < 10000 || p.RefEndMS > 30000 || result.Reference.StartMS != 10000 || result.Inputs[0].StartMS != 5000 {
		t.Fatalf("windowed placement: %+v in %+v", p, result)
	}
}

func TestSyncAudioRefusesUnrelatedAndReportsEachInput(t *testing.T) {
	analysisTools(t)
	song := syncSong(30, 3)
	reference := syncWAV(t, 30, map[int][]float64{0: song})
	same := syncWAV(t, 32, map[int][]float64{48000: song})
	other := syncWAV(t, 30, map[int][]float64{0: syncSong(30, 4)})
	result, err := syncEngine().SyncAudio(context.Background(), reference, []string{same, other}, SyncOptions{})
	if CodeOf(err) != CodeNoMatch {
		t.Fatalf("code %q, error %v", CodeOf(err), err)
	}
	if len(result.Inputs) != 2 || result.Inputs[0].Status != "match" || math.Abs(result.Inputs[0].Placements[0].OffsetMS-1000) > 2 {
		t.Fatalf("matching input: %+v", result.Inputs)
	}
	if result.Inputs[1].Status != "no_match" || result.Inputs[1].Placements == nil || len(result.Inputs[1].Placements) != 0 {
		t.Fatalf("unrelated input: %+v", result.Inputs[1])
	}
}

func TestSyncAudioReportsAHookHeardTwice(t *testing.T) {
	analysisTools(t)
	hook := syncSong(10, 5)
	reference := syncWAV(t, 10, map[int][]float64{0: hook})
	input := syncWAV(t, 60, map[int][]float64{8 * 48000: hook, 41*48000 + 24000: hook, 0: syncSong(60, 6)})
	e := syncEngine()
	result, err := e.SyncAudio(context.Background(), reference, []string{input}, SyncOptions{})
	if CodeOf(err) != CodeNoMatch || result.Inputs[0].Status != "ambiguous" || len(result.Inputs[0].Placements) < 2 {
		t.Fatalf("without all: %v, %+v", err, result.Inputs)
	}
	result, err = e.SyncAudio(context.Background(), reference, []string{input}, SyncOptions{All: true})
	if err != nil || result.Inputs[0].Status != "match" || len(result.Inputs[0].Placements) < 2 {
		t.Fatalf("with all: %v, %+v", err, result.Inputs)
	}
	found := 0
	for _, p := range result.Inputs[0].Placements[:2] {
		if math.Abs(p.OffsetMS-8000) < 2 || math.Abs(p.OffsetMS-41500) < 2 {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("placements: %+v", result.Inputs[0].Placements)
	}
}

func TestAnalyzeRepeatsFindsReturningSection(t *testing.T) {
	analysisTools(t)
	hook := syncSong(10, 7)
	const delta = 887256 // 18.4845 s
	input := syncWAV(t, 60, map[int][]float64{0: syncSong(14, 8), 14 * 48000: hook, 24 * 48000: syncSong(8.4, 9), 14*48000 + delta: hook, 43 * 48000: syncSong(16, 10)})
	result, err := syncEngine().AnalyzeRepeats(context.Background(), input, RepeatsOptions{})
	if err != nil || result.Version != 1 || len(result.Repeats) == 0 {
		t.Fatalf("repeats: %v, %+v", err, result)
	}
	r := result.Repeats[0]
	if math.Abs(r.DeltaMS-18484.5) > 2 || r.StartMS < 11000 || r.EndMS > 27000 {
		t.Fatalf("repeat: %+v", r)
	}
	if again, err := syncEngine().AnalyzeRepeats(context.Background(), syncWAV(t, 30, map[int][]float64{0: syncSong(30, 11)}), RepeatsOptions{}); err != nil || again.Repeats == nil || len(again.Repeats) != 0 {
		t.Fatalf("recording without repeats: %v, %+v", err, again)
	}
}

func TestSyncAudioValidatesArgumentsAndAudio(t *testing.T) {
	analysisTools(t)
	reference := syncWAV(t, 5, map[int][]float64{0: syncSong(5, 12)})
	e := syncEngine()
	if _, err := e.SyncAudio(context.Background(), reference, nil, SyncOptions{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("no inputs: %v", err)
	}
	if _, err := e.SyncAudio(context.Background(), reference, []string{reference}, SyncOptions{Band: "bass"}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("band: %v", err)
	}
	if _, err := e.SyncAudio(context.Background(), reference, []string{filepath.Join(t.TempDir(), "absent.wav")}, SyncOptions{}); CodeOf(err) != CodeInputNotFound {
		t.Fatalf("absent input: %v", err)
	}
	short := syncWAV(t, 0.5, map[int][]float64{0: syncSong(0.5, 13)})
	if _, err := e.SyncAudio(context.Background(), reference, []string{short}, SyncOptions{}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("short input: %v", err)
	}
	silent := filepath.Join(t.TempDir(), "-silent.mp4")
	analysisFixtureCommand(t, "-f", "lavfi", "-i", "color=black:s=160x90:r=25:d=2", "-c:v", "libx264", "-threads", "1", "-an", silent)
	if _, err := e.SyncAudio(context.Background(), reference, []string{silent}, SyncOptions{}); CodeOf(err) != CodeAudioMissing {
		t.Fatalf("input without audio: %q %v", CodeOf(err), err)
	}
	if _, err := e.AnalyzeRepeats(context.Background(), reference, RepeatsOptions{MinGapMS: 10}); CodeOf(err) != CodeInvalidArgument {
		t.Fatalf("min gap: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.SyncAudio(ctx, reference, []string{reference}, SyncOptions{}); CodeOf(err) != CodeCancelled {
		t.Fatalf("cancelled: %v", err)
	}
}
