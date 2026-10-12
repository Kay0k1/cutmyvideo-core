package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
)

const waveformStepMS int64 = 100
const waveformSampleRate = 4000
const waveformOutputBytes int64 = 64 << 10
const waveformMaxChannels = 32

type waveformData struct {
	StartMS    int64     `json:"start_ms"`
	EndMS      int64     `json:"end_ms"`
	StepMS     int64     `json:"step_ms"`
	HasAudio   bool      `json:"has_audio"`
	AudioTrack *int      `json:"audio_track,omitempty"`
	Peaks      []float64 `json:"peaks"`
}

// Waveforms use the same short source intervals, guarded network budget,
// background admission, private cache and durable cleanup as preview windows.
// This deliberately never starts an unbounded whole-recording decode.
func (s *Server) sourceWaveform(w http.ResponseWriter, r *http.Request, owner string) {
	s.sourceAnalysisWithAdmission(w, r, owner, windowWaveform, s.Store.publishPreview, s.Store.acquirePreviewAfterCacheLookup)
}

// pcmPeaks retains only one maximum per 100 ms. Split 16-bit samples across
// writes are joined; the decoder cannot grow this fixed-size result buffer.
type pcmPeaks struct {
	peaks      []float64
	samples    int64
	maxSamples int64
	channels   int64
	pending    byte
	hasPending bool
}

func (p *pcmPeaks) Write(data []byte) (int, error) {
	length := len(data)
	for _, b := range data {
		if !p.hasPending {
			p.pending, p.hasPending = b, true
			continue
		}
		if p.samples >= p.maxSamples {
			return 0, errors.New("waveform decode exceeded its interval")
		}
		value := int16(uint16(p.pending) | uint16(b)<<8)
		amplitude := math.Abs(float64(value)) / 32768
		index := p.samples / (max(1, p.channels) * waveformSampleRate * waveformStepMS / 1000)
		if index < int64(len(p.peaks)) && amplitude > p.peaks[index] {
			p.peaks[index] = amplitude
		}
		p.samples++
		p.hasPending = false
	}
	return length, nil
}

func renderWaveformOptions(ctx context.Context, c Config, inputs []mediaInput, interval Range, out string, request ExportRequest) error {
	if interval.EndMS <= interval.StartMS || interval.EndMS-interval.StartMS > previewWindowMS {
		return errors.New("waveform interval must be a bounded source window")
	}
	selection, err := probeInputSelectionOptions(ctx, c, inputs, request)
	if err != nil {
		return err
	}
	data := waveformData{StartMS: interval.StartMS, EndMS: interval.EndMS, StepMS: waveformStepMS,
		HasAudio: selection.Audio != nil, Peaks: make([]float64, (interval.EndMS-interval.StartMS+waveformStepMS-1)/waveformStepMS)}
	if selection.Audio != nil {
		channels := selection.Audio.Channels
		if channels < 1 || channels > waveformMaxChannels {
			return errUnsupportedStream
		}
		index := selection.Audio.Index
		data.AudioTrack = &index
		input := inputs[selection.AudioInput]
		if interval.StartMS < input.OffsetMS {
			return errUnsupportedStream
		}
		protocols := "file"
		if input.Remote {
			protocols = "http,tcp"
		}
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-max_alloc", "268435456", "-filter_threads", "1",
			"-protocol_whitelist", protocols, "-format_whitelist", mediaFormats}
		args = append(args, mediaInputBounds(c)...)
		args = append(args, "-ss", seconds(interval.StartMS-input.OffsetMS), "-i", input.Path,
			"-t", seconds(interval.EndMS-interval.StartMS), "-map", fmt.Sprintf("0:%d", index), "-vn", "-sn", "-dn",
			"-af", "aresample=4000:async=1:first_pts=0", "-ar", "4000", "-threads", "1", "-c:a", "pcm_s16le", "-f", "s16le", "pipe:1")
		// Preserve every selected channel and take its absolute peak. A mono
		// downmix can cancel antiphase stereo, falsely displaying silence.
		peaks := &pcmPeaks{peaks: data.Peaks, channels: int64(channels), maxSamples: ((interval.EndMS-interval.StartMS)*waveformSampleRate/1000 + waveformSampleRate/10) * int64(channels)}
		if err := runCommandOutput(ctx, c.FFmpeg, args, peaks); err != nil {
			return err
		}
		if peaks.hasPending || peaks.samples%int64(channels) != 0 {
			return errors.New("incomplete waveform sample")
		}
		for i, peak := range data.Peaks {
			data.Peaks[i] = math.Round(peak*10000) / 10000
		}
	}
	bytes, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if int64(len(bytes)) >= waveformOutputBytes {
		return errOutputLimit
	}
	return os.WriteFile(out, bytes, 0600)
}
