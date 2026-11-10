package localmedia

import (
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/audiosync"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

// SyncMaxInputs bounds one request. Every recording is analysed within the
// usual 30 minute window; choose windows explicitly for longer recordings.
const SyncMaxInputs = 16

// SyncOptions selects source windows and the voting bands. Zero ends use the
// recording's duration. Band is auto, full, vocal or drums; empty means auto.
// All lists every placement nearly as strong as the best one, for a reference
// that occurs more than once.
type SyncOptions struct {
	RefStartMS int64  `json:"ref_start_ms"`
	RefEndMS   int64  `json:"ref_end_ms"`
	StartMS    int64  `json:"start_ms"`
	EndMS      int64  `json:"end_ms"`
	Band       string `json:"band,omitempty"`
	All        bool   `json:"all,omitempty"`
}

// SyncPlacement maps reference time to input time on the original timelines:
// input_ms = ref_ms + offset_ms. With speed other than 1 the mapping is linear
// between the matched range ends and offset_ms holds at the range middle.
// Method is waveform when both recordings share a master and the offset is
// accurate to the sample, envelope when only attacks agree (a remix, another
// encode with heavy processing). The matched range resolves to a few seconds.
type SyncPlacement struct {
	OffsetMS     float64 `json:"offset_ms"`
	Speed        float64 `json:"speed"`
	Confidence   float64 `json:"confidence"`
	Similarity   float64 `json:"similarity"`
	Band         string  `json:"band"`
	Method       string  `json:"method"`
	RefStartMS   int64   `json:"ref_start_ms"`
	RefEndMS     int64   `json:"ref_end_ms"`
	InputStartMS int64   `json:"input_start_ms"`
	InputEndMS   int64   `json:"input_end_ms"`
	Chunks       int     `json:"chunks"`
	Agreeing     int     `json:"agreeing"`
}

// SyncSource reports what was actually decoded from one recording.
type SyncSource struct {
	Path          string `json:"path"`
	AudioTrack    int    `json:"audio_track"`
	StartMS       int64  `json:"start_ms"`
	EndMS         int64  `json:"end_ms"`
	AnalyzedEndMS int64  `json:"analyzed_end_ms"`
}

// SyncInput is one input's outcome. Status is match, ambiguous or no_match.
// A match usually has one placement; an input edited from the reference has
// one placement per part, each over its own reference range. Ambiguous means
// the same part of the reference fits several offsets equally well, as a hook
// that is heard twice: the placements are listed so a caller can choose with
// knowledge the audio does not carry, and All accepts them as a match.
type SyncInput struct {
	SyncSource
	Status     string          `json:"status"`
	Placements []SyncPlacement `json:"placements"`
}

type SyncResult struct {
	Version   int         `json:"version"`
	Reference SyncSource  `json:"reference"`
	Inputs    []SyncInput `json:"inputs"`
}

// RepeatsOptions analyses one recording against itself. MinGapMS ignores
// repeats closer than this; zero means 2000. Band is full, vocal or drums;
// empty means full.
type RepeatsOptions struct {
	StartMS  int64  `json:"start_ms"`
	EndMS    int64  `json:"end_ms"`
	MinGapMS int64  `json:"min_gap_ms"`
	Band     string `json:"band,omitempty"`
}

// Repeat says the material at [StartMS, EndMS) is heard again DeltaMS later.
type Repeat struct {
	DeltaMS    float64 `json:"delta_ms"`
	StartMS    int64   `json:"start_ms"`
	EndMS      int64   `json:"end_ms"`
	Confidence float64 `json:"confidence"`
	Similarity float64 `json:"similarity"`
	Method     string  `json:"method"`
}

type RepeatsResult struct {
	Version int `json:"version"`
	SyncSource
	Repeats []Repeat `json:"repeats"`
}

func syncBand(band string) (string, error) {
	switch band {
	case "", "auto":
		return "auto", nil
	case "full", "vocal", "drums":
		return band, nil
	}
	return "", analysisArgument("band must be auto, full, vocal or drums")
}

// SyncAudio places a reference recording inside each input by sound. It never
// writes media. A missing or ambiguous placement is reported per input; the
// caller decides whether that fails its operation.
func SyncAudio(ctx context.Context, c Config, reference string, inputs []string, options SyncOptions) (SyncResult, error) {
	band, err := syncBand(options.Band)
	if err != nil {
		return SyncResult{}, err
	}
	if len(inputs) == 0 || len(inputs) > SyncMaxInputs {
		return SyncResult{}, analysisArgument("sync requires 1 through 16 inputs")
	}
	result := SyncResult{Version: 1, Inputs: make([]SyncInput, 0, len(inputs))}
	ref, source, err := syncFeatures(ctx, c, reference, options.RefStartMS, options.RefEndMS)
	if err != nil {
		return SyncResult{}, err
	}
	result.Reference = source
	for _, path := range inputs {
		in, source, err := syncFeatures(ctx, c, path, options.StartMS, options.EndMS)
		if err != nil {
			return SyncResult{}, err
		}
		matches := audiosync.Align(ref, in, audiosync.Options{Band: band, Thorough: options.All, Workers: syncWorkers(c)})
		if err = ctx.Err(); err != nil {
			return SyncResult{}, err
		}
		entry := SyncInput{SyncSource: source, Status: "no_match", Placements: make([]SyncPlacement, 0, len(matches))}
		shift := float64(source.StartMS - result.Reference.StartMS)
		for _, m := range matches {
			entry.Placements = append(entry.Placements, SyncPlacement{
				OffsetMS:     math.Round((m.OffsetSeconds*1000+shift)*1000)/1000 + 0,
				Speed:        math.Round(m.Speed*1e6) / 1e6,
				Confidence:   math.Round(m.Confidence*1000) / 1000,
				Similarity:   math.Round(m.Similarity*1000) / 1000,
				Band:         m.Band,
				Method:       m.Method,
				RefStartMS:   result.Reference.StartMS + int64(math.Round(m.RefStart*1000)),
				RefEndMS:     result.Reference.StartMS + int64(math.Round(m.RefEnd*1000)),
				InputStartMS: source.StartMS + int64(math.Round(m.InputStart*1000)),
				InputEndMS:   source.StartMS + int64(math.Round(m.InputEnd*1000)),
				Chunks:       m.Chunks,
				Agreeing:     m.Agreeing,
			})
		}
		switch {
		case len(matches) == 0:
		case options.All || !audiosync.Ambiguous(matches):
			entry.Status = "match"
		default:
			entry.Status = "ambiguous"
		}
		result.Inputs = append(result.Inputs, entry)
	}
	return result, nil
}

// AnalyzeRepeats finds material that returns later in the same recording, such
// as a chorus, with the same offset accuracy as SyncAudio.
func AnalyzeRepeats(ctx context.Context, c Config, path string, options RepeatsOptions) (RepeatsResult, error) {
	if options.MinGapMS == 0 {
		options.MinGapMS = 2000
	}
	if options.MinGapMS < 500 || options.MinGapMS > AnalysisMaxWindowMS {
		return RepeatsResult{}, analysisArgument("min_gap_ms must be 500 through 1800000")
	}
	band := options.Band
	if band == "" {
		band = "full"
	}
	if band != "full" && band != "vocal" && band != "drums" {
		return RepeatsResult{}, analysisArgument("repeats band must be full, vocal or drums")
	}
	features, source, err := syncFeatures(ctx, c, path, options.StartMS, options.EndMS)
	if err != nil {
		return RepeatsResult{}, err
	}
	matches := audiosync.Align(features, features, audiosync.Options{Band: band, MinLagSeconds: float64(options.MinGapMS) / 1000, MinSimilarity: 0.4, Workers: syncWorkers(c)})
	if err = ctx.Err(); err != nil {
		return RepeatsResult{}, err
	}
	result := RepeatsResult{Version: 1, SyncSource: source, Repeats: make([]Repeat, 0, len(matches))}
	for _, m := range matches {
		result.Repeats = append(result.Repeats, Repeat{
			DeltaMS:    math.Round(m.OffsetSeconds*1e6) / 1000,
			StartMS:    source.StartMS + int64(math.Round(m.RefStart*1000)),
			EndMS:      source.StartMS + int64(math.Round(m.RefEnd*1000)),
			Confidence: math.Round(m.Confidence*1000) / 1000,
			Similarity: math.Round(m.Similarity*1000) / 1000,
			Method:     m.Method,
		})
	}
	return result, nil
}

func syncWorkers(c Config) int {
	return min(8, max(1, c.FFmpegThreads))
}

// syncFeatures decodes the default audio stream of a window as mono 16 kHz
// float PCM and analyses it. Stereo is downmixed by FFmpeg; a recording whose
// channels cancel each other has nothing to align.
func syncFeatures(ctx context.Context, c Config, path string, startMS, endMS int64) (*audiosync.Features, SyncSource, error) {
	ctx, session, info, duration, err := analysisSource(ctx, c, path)
	if err != nil {
		return nil, SyncSource{}, err
	}
	defer session.Close()
	start, end, err := analysisWindow(startMS, endMS, duration)
	if err != nil {
		return nil, SyncSource{}, err
	}
	audio := info.selectedAudio()
	if audio == nil {
		return nil, SyncSource{}, mediaerror.ErrNoAudio
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, SyncSource{}, err
	}
	pcm := &syncPCM{samples: make([]float32, 0, (end-start)*audiosync.PCMRate/1000+audiosync.PCMRate), limit: int((end-start)*audiosync.PCMRate/1000) + audiosync.PCMRate}
	args := analysisInputArgs(c, session.Path, start, end)
	rate := strconv.Itoa(audiosync.PCMRate)
	args = append(args, "-map", "0:"+strconv.Itoa(audio.Index), "-vn", "-sn", "-dn", "-af", "aresample="+rate+":async=1:first_pts=0", "-ac", "1", "-ar", rate, "-threads", "1", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	if err = runAnalysis(ctx, c, args, pcm); err != nil {
		return nil, SyncSource{}, err
	}
	if err = analysisUnchanged(session); err != nil {
		return nil, SyncSource{}, err
	}
	if pcm.partialCount != 0 {
		return nil, SyncSource{}, analysisMalformed()
	}
	features, err := audiosync.Extract(pcm.samples)
	if err != nil {
		return nil, SyncSource{}, analysisArgument("sync needs at least one second of decoded audio in every recording")
	}
	source := SyncSource{Path: absolute, AudioTrack: audio.Index, StartMS: start, EndMS: end, AnalyzedEndMS: min(end, start+int64(len(pcm.samples))*1000/audiosync.PCMRate)}
	return features, source, nil
}

// syncPCM collects little-endian float samples that may be split across writes.
type syncPCM struct {
	samples      []float32
	limit        int
	partial      [4]byte
	partialCount int
}

func (p *syncPCM) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 {
		var bits uint32
		if p.partialCount != 0 || len(data) < 4 {
			n := copy(p.partial[p.partialCount:], data)
			p.partialCount += n
			data = data[n:]
			if p.partialCount != 4 {
				continue
			}
			bits, p.partialCount = binary.LittleEndian.Uint32(p.partial[:]), 0
		} else {
			bits, data = binary.LittleEndian.Uint32(data), data[4:]
		}
		value := math.Float32frombits(bits)
		if !finite(float64(value)) || value > 16 || value < -16 {
			return 0, analysisMalformed()
		}
		if len(p.samples) >= p.limit {
			return 0, analysisLimit("audio decoder exceeded the requested interval")
		}
		p.samples = append(p.samples, value)
	}
	return length, nil
}
