package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestWaveformPCMStreamingBoundsAndSplitSamples(t *testing.T) {
	p := &pcmPeaks{peaks: make([]float64, 2), maxSamples: 800}
	data := make([]byte, 1600)
	for i := 400; i < 800; i++ {
		binary.LittleEndian.PutUint16(data[i*2:], uint16(16384))
	}
	for _, part := range [][]byte{data[:1], data[1:799], data[799:]} {
		if _, err := p.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if p.samples != 800 || p.hasPending || p.peaks[0] != 0 || p.peaks[1] != .5 {
		t.Fatalf("incorrect sample bins: %+v", p)
	}
	if _, err := p.Write([]byte{0, 0}); err == nil {
		t.Fatal("unbounded PCM accepted")
	}
}

func waveformFixture(t *testing.T, c Config) string {
	t.Helper()
	path := filepath.Join(c.DataDir, "waveform-source.wav")
	// Independent PCM witness: silence, sine, silence, louder sine.
	const samples = 4 * waveformSampleRate
	data := make([]byte, 44+samples*2)
	copy(data, "RIFF")
	binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
	copy(data[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(data[16:], 16)
	binary.LittleEndian.PutUint16(data[20:], 1)
	binary.LittleEndian.PutUint16(data[22:], 1)
	binary.LittleEndian.PutUint32(data[24:], waveformSampleRate)
	binary.LittleEndian.PutUint32(data[28:], waveformSampleRate*2)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[34:], 16)
	copy(data[36:], "data")
	binary.LittleEndian.PutUint32(data[40:], samples*2)
	for i := 0; i < samples; i++ {
		amplitude := 0.0
		if i >= waveformSampleRate && i < 2*waveformSampleRate {
			amplitude = .4
		}
		if i >= 3*waveformSampleRate {
			amplitude = .8
		}
		value := int16(amplitude * 32767 * math.Sin(2*math.Pi*100*float64(i)/waveformSampleRate))
		binary.LittleEndian.PutUint16(data[44+i*2:], uint16(value))
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readWaveform(t *testing.T, path string) waveformData {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var wave waveformData
	if err := json.Unmarshal(data, &wave); err != nil {
		t.Fatal(err)
	}
	return wave
}

func TestWaveformMatchesIndependentAudioTimelineAndOffsets(t *testing.T) {
	c := mediaConfig(t)
	input := waveformFixture(t, c)
	out := filepath.Join(c.DataDir, "waveform.json")
	offset := int64(8 * 3600000)
	if err := renderWaveformOptions(context.Background(), c, []mediaInput{{Path: input, OffsetMS: offset}}, Range{StartMS: offset, EndMS: offset + 4000}, out, ExportRequest{}); err != nil {
		t.Fatal(err)
	}
	wave := readWaveform(t, out)
	if !wave.HasAudio || wave.StartMS != offset || wave.EndMS != offset+4000 || wave.StepMS != 100 || len(wave.Peaks) != 40 {
		t.Fatalf("invalid waveform: %+v", wave)
	}
	for i, peak := range wave.Peaks {
		wanted := 0.0
		if i >= 10 && i < 20 {
			wanted = .4
		}
		if i >= 30 {
			wanted = .8
		}
		if math.Abs(peak-wanted) > .002 {
			t.Fatalf("bin %d peak %f want %f", i, peak, wanted)
		}
	}
	if err := renderWaveformOptions(context.Background(), c, []mediaInput{{Path: input}}, Range{StartMS: 1000, EndMS: 2050}, out, ExportRequest{}); err != nil {
		t.Fatal(err)
	}
	wave = readWaveform(t, out)
	if len(wave.Peaks) != 11 || math.Abs(wave.Peaks[0]-.4) > .002 || wave.Peaks[10] > .002 {
		t.Fatalf("seek/near-end mismatch: %+v", wave)
	}
	if err := renderWaveformOptions(context.Background(), c, []mediaInput{{Path: input}}, Range{StartMS: 0, EndMS: 4000}, out, ExportRequest{NoAudio: true}); err != nil {
		t.Fatal(err)
	}
	wave = readWaveform(t, out)
	if wave.HasAudio {
		t.Fatal("silent selection returned audio")
	}
	for _, peak := range wave.Peaks {
		if peak != 0 {
			t.Fatal("silent selection returned nonzero bins")
		}
	}
}

func TestWaveformSelectsActualAudioTrack(t *testing.T) {
	c := mediaConfig(t)
	input := filepath.Join(c.DataDir, "tracks.mkv")
	_, err := runCommand(context.Background(), c.FFmpeg, "-v", "error", "-filter_threads", "1", "-f", "lavfi", "-i", "color=size=160x90:duration=2", "-f", "lavfi", "-i", "anullsrc=r=8000:cl=mono", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=8000", "-map", "0:v", "-map", "1:a", "-map", "2:a", "-t", "2", "-c:v", "libx264", "-preset", "ultrafast", "-threads", "1", "-c:a", "pcm_s16le", input)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(c.DataDir, "waveform.json")
	for _, index := range []int{1, 2} {
		if err := renderWaveformOptions(context.Background(), c, []mediaInput{{Path: input}}, Range{StartMS: 0, EndMS: 2000}, out, ExportRequest{AudioTrack: &index}); err != nil {
			t.Fatal(err)
		}
		wave := readWaveform(t, out)
		if wave.AudioTrack == nil || *wave.AudioTrack != index {
			t.Fatal("selection lost")
		}
		if index == 1 && wave.Peaks[5] != 0 {
			t.Fatal("silent track contains sound")
		}
		if index == 2 && wave.Peaks[5] < .1 {
			t.Fatal("selected tone missing")
		}
	}
}

func TestWaveformPrivateCacheAccountingAndDeletion(t *testing.T) {
	store := testStore(t)
	c := mediaConfig(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 1<<30, 1<<30
	cookie := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	auth := httptest.NewRequest(http.MethodGet, "/", nil)
	auth.AddCookie(&http.Cookie{Name: "cutmy_session", Value: cookie})
	owner, _ := ownerFromRequest(auth)
	source := Source{ID: newID("src"), Owner: owner, Title: "Wave", Kind: "upload", DurationMS: 4000, Path: waveformFixture(t, c)}
	if err := store.AddSource(context.Background(), source); err != nil {
		t.Fatal(err)
	}
	app := NewServer(c, store)
	request := func(session string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/sources/"+source.ID+"/waveform?start_ms=1000", nil)
		r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: session})
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, r)
		return w
	}
	first := request(cookie)
	if first.Code != 200 || first.Header().Get("Content-Type") != "application/json" || first.Header().Get("X-Waveform-Step-MS") != "100" {
		t.Fatalf("waveform failed %d %s", first.Code, first.Body.String())
	}
	app.Config.FFmpeg, app.Config.FFprobe = "/unavailable", "/unavailable"
	second := request(cookie)
	if second.Code != 200 || second.Header().Get("X-Preview-Cache") != "hit" || !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatal("warm waveform did not reuse private cache")
	}
	foreign := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	if got := request(foreign); got.Code != 404 {
		t.Fatal("foreign waveform exposed")
	}
	var charged int64
	if err := store.DB.QueryRow(context.Background(), "SELECT sum(size_bytes) FROM storage_reservations WHERE kind='preview_cache'").Scan(&charged); err != nil || charged != int64(first.Body.Len()) {
		t.Fatalf("waveform accounting %d %v", charged, err)
	}
	if _, err := store.DeleteSource(context.Background(), source.ID, owner); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(c.DataDir, "previews"))
	if err != nil || len(entries) != 0 {
		t.Fatal("waveform survived source deletion")
	}
}
