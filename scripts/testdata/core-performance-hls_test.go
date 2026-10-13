package app

// Opt-in observation helper compiled through a Go overlay in each checkout.
// Both sides use this exact source. The separately hashed scope adapter is the
// sole difference: no-op on baseline, the operation cache on candidate.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type corePerfHLSTransport func(*http.Request) (*http.Response, error)

func (f corePerfHLSTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCorePerformanceHLSObservation(t *testing.T) {
	scenarioPath := os.Getenv("CUTMY_CORE_PERF_HLS_SCENARIO")
	if scenarioPath == "" {
		t.Skip("opt-in owned fixture observation")
	}
	var scenario struct {
		Ranges     []Range `json:"ranges"`
		OutputMode string  `json:"output_mode"`
		Options    struct {
			Format, Quality string
			CutMode         string `json:"cut_mode"`
		} `json:"options"`
		Warmup  bool `json:"warmup"`
		Network struct {
			BaseDelayMS      int64  `json:"base_delay_ms"`
			StragglerPath    string `json:"straggler_path"`
			StragglerDelayMS int64  `json:"straggler_delay_ms"`
		} `json:"network"`
	}
	blob, err := os.ReadFile(scenarioPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(blob, &scenario); err != nil {
		t.Fatal(err)
	}
	fixture := os.Getenv("CUTMY_CORE_PERF_HLS_FIXTURE")
	work := os.Getenv("CUTMY_CORE_PERF_HLS_WORK")
	resultFile := os.Getenv("CUTMY_CORE_PERF_HLS_RESULT")
	if fixture == "" || work == "" || resultFile == "" || len(scenario.Ranges) != 8 {
		t.Fatal("owned fixture, workspace, result and eight frozen ranges are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 170*time.Second)
	defer cancel()
	c := Config{FFmpeg: os.Getenv("FFMPEG_PATH"), FFprobe: os.Getenv("FFPROBE_PATH"), FFmpegProfile: "fast", FFmpegThreads: 2,
		MaxOutputBytes: 64 << 20, MaxSourceBytes: 64 << 20, MaxFetchBytes: 64 << 20, MaxRanges: 128, MaxRangeMS: 600000, MaxJobMS: 3600000, DataDir: work}
	g, err := newNetworkGuard(64 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	var mu sync.Mutex
	requests := []map[string]any{}
	begin := time.Now()
	g.client.Transport = corePerfHLSTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || r.URL.Host != "media.example" || strings.Contains(r.URL.Path, "..") || r.URL.RawQuery != "" {
			return nil, errors.New("unexpected performance fixture address")
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		if filepath.Base(name) != name || name == "" {
			return nil, errors.New("unexpected nested performance fixture path")
		}
		data, err := os.ReadFile(filepath.Join(fixture, name))
		if err != nil {
			return nil, err
		}
		started := time.Since(begin).Microseconds()
		delay := time.Duration(scenario.Network.BaseDelayMS) * time.Millisecond
		if name == scenario.Network.StragglerPath {
			delay += time.Duration(scenario.Network.StragglerDelayMS) * time.Millisecond
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(delay):
		}
		mu.Lock()
		requests = append(requests, map[string]any{"path": name, "bytes": len(data), "start_us": started, "headers_us": time.Since(begin).Microseconds()})
		mu.Unlock()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: http.Header{}}, nil
	})
	ctx, release, err := corePerformanceHLSScope(ctx, work)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	f := platformFormat{URL: "https://media.example/index.m3u8", Protocol: "m3u8_native", VCodec: "h264", ACodec: "aac", Ext: "mp4"}
	p, err := loadHLS(ctx, g, f, "best", 0)
	if err != nil {
		t.Fatal(err)
	}
	if scenario.Warmup {
		for index, r := range scenario.Ranges {
			_, _, err := stageHLS(ctx, c, g, f, p, r, work, 100+index)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	mu.Lock()
	prefillRequests := len(requests)
	mu.Unlock()
	phaseBegin, networkBefore := time.Now(), g.bytes.Load()
	inputs := make([][]mediaInput, len(scenario.Ranges))
	for index, r := range scenario.Ranges {
		path, offset, err := stageHLS(ctx, c, g, f, p, r, work, index)
		if err != nil {
			t.Fatal(err)
		}
		inputs[index] = []mediaInput{{Path: path, OffsetMS: offset}}
	}
	request := ExportRequest{Format: scenario.Options.Format, Quality: scenario.Options.Quality, CutMode: scenario.Options.CutMode}
	outputs := []map[string]any{}
	if scenario.OutputMode == "joined" {
		out := filepath.Join(work, "joined."+request.Format)
		segments, err := exportJoinedSegmentInputsProgress(ctx, c, inputs, scenario.Ranges, request, out, nil)
		if err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, map[string]any{"path": out, "segments": segments})
	} else {
		for index, r := range scenario.Ranges {
			out := filepath.Join(work, "piece-"+string(rune('a'+index))+"."+request.Format)
			start, end, err := exportInputs(ctx, c, inputs[index], r, request, out)
			if err != nil {
				t.Fatal(err)
			}
			outputs = append(outputs, map[string]any{"path": out, "actual_start_ms": start, "actual_end_ms": end,
				"segments": []ArtifactSegment{{Index: index, Label: r.Label, SourceStartMS: start, SourceEndMS: end, OutputStartMS: 0, OutputEndMS: end - start}}})
		}
	}
	mu.Lock()
	finalRequests := append([]map[string]any(nil), requests...)
	mu.Unlock()
	result := map[string]any{"outputs": outputs, "hls": map[string]any{"operation_wall_seconds": time.Since(begin).Seconds(),
		"measured_phase_wall_seconds": time.Since(phaseBegin).Seconds(), "actual_network_bytes": g.bytes.Load(),
		"measured_phase_network_bytes": g.bytes.Load() - networkBefore, "prefill_requests": prefillRequests,
		"measured_phase_requests": len(finalRequests) - prefillRequests, "requests": finalRequests,
		"fixture_transport": "fixed HTTPS media.example bytes; guarded URI/budget/staging/export paths; no external network"}}
	blob, err = json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultFile, append(blob, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
