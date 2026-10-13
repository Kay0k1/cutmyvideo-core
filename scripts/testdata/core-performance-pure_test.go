package app

// Identical overlay source for baseline/candidate. These are pure Go costs,
// measured separately from the media workflow and never used as export SLAs.
import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"testing"
)

const corePerfPCMBytes = 32 << 20
const corePerfPCMBins = 2000

var corePerfPeakSink []float64
var corePerfSubtitleSink []byte

func corePerfPCMFixture() []byte {
	data := make([]byte, corePerfPCMBytes)
	state := uint32(0x524f4f54)
	for index := range data {
		state = state*1664525 + 1013904223
		data[index] = byte(state >> 24)
	}
	return data
}

func corePerfPeakDigest(peaks []float64) [32]byte {
	data := make([]byte, len(peaks)*8)
	for index, value := range peaks {
		binary.LittleEndian.PutUint64(data[index*8:], math.Float64bits(value))
	}
	return sha256.Sum256(data)
}

func TestCorePerformancePureWitness(t *testing.T) {
	data := corePerfPCMFixture()
	expected := make([]float64, corePerfPCMBins)
	for index := 0; index < len(data); index += 2 {
		value := int32(int16(binary.LittleEndian.Uint16(data[index:])))
		if value < 0 {
			value = -value
		}
		bin := int64(index/2/2) * 1000 / (44100 * 100)
		if bin < int64(len(expected)) {
			expected[bin] = max(expected[bin], float64(value)/32768)
		}
	}
	peaks := pcmPeaks{peaks: make([]float64, corePerfPCMBins), channels: 2, sampleRate: 44100, maxSamples: int64(len(data) / 2)}
	// Odd chunks independently exercise pending half-samples and channel/bin
	// boundaries; the timed benchmark uses the ordinary 64 KiB writer chunks.
	for offset := 0; offset < len(data); {
		end := min(len(data), offset+65535)
		if count, err := peaks.Write(data[offset:end]); err != nil || count != end-offset {
			t.Fatal(count, err)
		}
		offset = end
	}
	if peaks.hasPending || peaks.samples != int64(len(data)/2) || corePerfPeakDigest(peaks.peaks) != corePerfPeakDigest(expected) {
		t.Fatal("PCM aggregate differs from the independent integer-sample reference")
	}
	cues, segments := corePerfSubtitleFixture()
	text, err := rebaseSubtitles(cues, segments)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pcm_input_sha256=%x pcm_peaks_sha256=%x subtitle_output_sha256=%x subtitle_output_bytes=%d", sha256.Sum256(data), corePerfPeakDigest(peaks.peaks), sha256.Sum256(text), len(text))
}

func BenchmarkCorePerformancePCM32MiB(b *testing.B) {
	data := corePerfPCMFixture()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		peaks := pcmPeaks{peaks: make([]float64, corePerfPCMBins), channels: 2, sampleRate: 44100, maxSamples: int64(len(data) / 2)}
		for offset := 0; offset < len(data); offset += 65536 {
			if count, err := peaks.Write(data[offset:min(len(data), offset+65536)]); err != nil || count != min(65536, len(data)-offset) {
				b.Fatal(count, err)
			}
		}
		corePerfPeakSink = peaks.peaks
	}
}

func corePerfSubtitleFixture() ([]subtitleCue, []ArtifactSegment) {
	cues := make([]subtitleCue, 10000)
	for index := range cues {
		start := int64(index) * 1000
		cues[index] = subtitleCue{StartMS: start, EndMS: start + 1250, Text: fmt.Sprintf("Controlled subtitle cue %05d", index)}
	}
	segments := make([]ArtifactSegment, 32)
	for index := range segments {
		start := int64((index*313)%9950)*1000 + 500
		segments[index] = ArtifactSegment{Index: index, SourceStartMS: start, SourceEndMS: start + 850, OutputStartMS: int64(index) * 850, OutputEndMS: int64(index+1) * 850}
	}
	return cues, segments
}

func BenchmarkCorePerformanceSubtitleRebase10000Cues32Cuts(b *testing.B) {
	cues, segments := corePerfSubtitleFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		text, err := rebaseSubtitles(cues, segments)
		if err != nil {
			b.Fatal(err)
		}
		corePerfSubtitleSink = text
	}
}
