package localmedia

import (
	"context"
	"encoding/binary"
	"math"
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

const analysisAudioRate int64 = 48000
const analysisAudioBinMS int64 = 10
const analysisAudioMaxSamples int64 = 256 * 1000 * 1000

// AnalyzeSilence inspects only local media and never creates an output file.
// It reports quiet energy, not speech recognition or a promise to retain words.
func AnalyzeSilence(ctx context.Context, c Config, path string, options SilenceOptions) (SilenceResult, error) {
	o, err := silenceOptions(options)
	if err != nil {
		return SilenceResult{}, err
	}
	ctx, session, info, duration, err := analysisSource(ctx, c, path)
	if err != nil {
		return SilenceResult{}, err
	}
	defer session.Close()
	start, end, err := analysisWindow(o.StartMS, o.EndMS, duration)
	if err != nil {
		return SilenceResult{}, err
	}
	audio := info.selectedAudio()
	if o.AudioTrack != nil {
		audio = nil
		for i := range info.Streams {
			if info.Streams[i].CodecType == "audio" && info.Streams[i].Index == *o.AudioTrack {
				audio = &info.Streams[i]
				break
			}
		}
		if audio == nil {
			return SilenceResult{}, &mediaerror.Problem{Code: "audio_track_unavailable", Message: "The selected absolute audio stream index is not available"}
		}
	}
	if audio == nil {
		return SilenceResult{}, mediaerror.ErrNoAudio
	}
	if audio.Channels < 1 || audio.Channels > 32 {
		return SilenceResult{}, analysisArgument("silence analysis supports 1 through 32 selected audio channels")
	}
	frames := ((end-start)*analysisAudioRate + 999) / 1000
	if frames > analysisAudioMaxSamples/int64(audio.Channels) {
		return SilenceResult{}, analysisLimit("silence analysis exceeds its 256 million decoded sample budget; choose a shorter interval")
	}
	pcm := newSilencePCM(start, end, int64(audio.Channels), o)
	args := analysisInputArgs(c, session.Path, start, end)
	args = append(args, "-map", "0:"+strconv.Itoa(audio.Index), "-vn", "-sn", "-dn", "-af", "aresample=48000:async=1:first_pts=0", "-ar", "48000", "-threads", "1", "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	if err = runAnalysis(ctx, c, args, pcm); err != nil {
		return SilenceResult{}, err
	}
	if err = analysisUnchanged(session); err != nil {
		return SilenceResult{}, err
	}
	if err = pcm.finish(); err != nil {
		return SilenceResult{}, err
	}
	result := SilenceResult{Version: 1, StartMS: start, EndMS: end, AnalyzedEndMS: pcm.coverage(), AudioTrack: audio.Index, Silence: pcm.silence}
	result.Keep = KeepAfterSilence(start, result.AnalyzedEndMS, result.Silence, o.KeepBeforeMS, o.KeepAfterMS, o.MinClipMS)
	return result, nil
}

// silencePCM retains per-channel energy and detected intervals, never a PCM
// recording. Four-byte samples and whole channel frames may cross writes.
type silencePCM struct {
	start, end, channels int64
	options              SilenceOptions
	threshold            float64
	partial              [4]byte
	partialCount         int
	channel              int64
	frames, binFrames    int64
	energy               [32]float64
	quietFrom            int64
	silence              []AnalysisInterval
}

func newSilencePCM(start, end, channels int64, options SilenceOptions) *silencePCM {
	return &silencePCM{start: start, end: end, channels: channels, options: options, threshold: math.Pow(10, options.ThresholdDB/10), quietFrom: -1, silence: make([]AnalysisInterval, 0)}
}

func (p *silencePCM) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 {
		if p.partialCount != 0 || len(data) < 4 {
			n := copy(p.partial[p.partialCount:], data)
			p.partialCount += n
			data = data[n:]
			if p.partialCount != 4 {
				continue
			}
			if err := p.sample(binary.LittleEndian.Uint32(p.partial[:])); err != nil {
				return 0, err
			}
			p.partialCount = 0
		} else {
			if err := p.sample(binary.LittleEndian.Uint32(data)); err != nil {
				return 0, err
			}
			data = data[4:]
		}
	}
	return length, nil
}

func (p *silencePCM) sample(bits uint32) error {
	if p.frames >= ((p.end-p.start)*analysisAudioRate+999)/1000 || p.frames*p.channels+p.channel >= analysisAudioMaxSamples {
		return analysisLimit("silence decoder exceeded the requested sample interval")
	}
	value := float64(math.Float32frombits(bits))
	if !finite(value) || math.Abs(value) > 16 {
		return analysisMalformed()
	}
	p.energy[p.channel] += value * value
	p.channel++
	if p.channel == p.channels {
		p.channel = 0
		p.frames++
		p.binFrames++
		if p.binFrames == analysisAudioRate*analysisAudioBinMS/1000 {
			return p.bin()
		}
	}
	return nil
}

func (p *silencePCM) coverage() int64 {
	return min(p.end, p.start+p.frames*1000/analysisAudioRate)
}

func (p *silencePCM) bin() error {
	quiet := true
	for channel := int64(0); channel < p.channels; channel++ {
		if p.energy[channel]/float64(p.binFrames) > p.threshold {
			quiet = false
		}
		p.energy[channel] = 0
	}
	from := p.start + (p.frames-p.binFrames)*1000/analysisAudioRate
	p.binFrames = 0
	if quiet && p.quietFrom < 0 {
		p.quietFrom = from
	}
	if !quiet {
		return p.endQuiet(from)
	}
	return nil
}

func (p *silencePCM) endQuiet(until int64) error {
	if p.quietFrom >= 0 && until-p.quietFrom >= p.options.MinSilenceMS {
		if len(p.silence) >= AnalysisMaxEvents {
			return analysisLimit("silence analysis exceeds 4096 detected intervals; choose a shorter interval or a larger minimum pause")
		}
		p.silence = append(p.silence, AnalysisInterval{StartMS: p.quietFrom, EndMS: until})
	}
	p.quietFrom = -1
	return nil
}

func (p *silencePCM) finish() error {
	if p.partialCount != 0 || p.channel != 0 {
		return analysisMalformed()
	}
	if p.frames == 0 {
		return analysisMalformed()
	}
	if p.binFrames > 0 {
		if err := p.bin(); err != nil {
			return err
		}
	}
	return p.endQuiet(p.coverage())
}
