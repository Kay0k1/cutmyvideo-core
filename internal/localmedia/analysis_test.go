package localmedia

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
)

func TestAnalysisPCMIndependentChannelEnergyAndSplitWrites(t *testing.T) {
	o, err := silenceOptions(SilenceOptions{ThresholdDB: -40, MinSilenceMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 3, 7, 1920, 5001} {
		p := newSilencePCM(1000, 1040, 2, o)
		var data []byte
		for frame := 0; frame < 1920; frame++ {
			value := float32(0)
			if frame >= 480 && frame < 1440 {
				value = 0.25
			}
			for _, sample := range []float32{value, -value} {
				data = binary.LittleEndian.AppendUint32(data, math.Float32bits(sample))
			}
		}
		for len(data) > 0 {
			n := min(chunk, len(data))
			if _, err := p.Write(data[:n]); err != nil {
				t.Fatal(err)
			}
			data = data[n:]
		}
		if err := p.finish(); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(p.silence) != "[{1000 1010} {1030 1040}]" || p.coverage() != 1040 {
			t.Fatalf("chunk %d: %+v coverage=%d", chunk, p.silence, p.coverage())
		}
	}
}

func TestAnalysisPCMRejectsPartialNonFiniteAndBudgetOverflow(t *testing.T) {
	o, _ := silenceOptions(SilenceOptions{})
	for _, bits := range []uint32{math.Float32bits(float32(math.NaN())), math.Float32bits(float32(math.Inf(1))), math.Float32bits(32)} {
		p := newSilencePCM(0, 10, 1, o)
		if _, err := p.Write(binary.LittleEndian.AppendUint32(nil, bits)); err == nil {
			t.Fatal("invalid PCM accepted")
		}
	}
	for _, data := range [][]byte{{0}, {0, 0, 0, 0}} {
		p := newSilencePCM(0, 10, 2, o)
		_, _ = p.Write(data)
		if err := p.finish(); err == nil {
			t.Fatal("incomplete sample/channel frame accepted")
		}
	}
	p := newSilencePCM(0, 10, 1, o)
	if _, err := p.Write(make([]byte, 481*4)); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("PCM interval bound lost", err)
	}
	p = newSilencePCM(0, 10, 1, o)
	p.silence = make([]AnalysisInterval, AnalysisMaxEvents)
	p.quietFrom = 0
	p.options.MinSilenceMS = 10
	if err := p.endQuiet(10); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("event bound lost", err)
	}
}

func TestAnalysisKeepMarginsAndEmptyTimeline(t *testing.T) {
	for _, tc := range []struct {
		silence                []AnalysisInterval
		before, after, minimum int64
		want                   string
	}{
		{nil, 0, 0, 0, "[{0 1000 }]"},
		{[]AnalysisInterval{{0, 1000}}, 100, 100, 0, "[]"},
		{[]AnalysisInterval{{0, 200}, {400, 700}, {900, 1000}}, 50, 30, 0, "[{150 430 } {650 930 }]"},
		{[]AnalysisInterval{{100, 900}}, 100, 100, 250, "[]"},
		{[]AnalysisInterval{{100, 200}}, 100, 100, 0, "[{0 1000 }]"},
	} {
		got := KeepAfterSilence(0, 1000, tc.silence, tc.before, tc.after, tc.minimum)
		if fmt.Sprint(got) != tc.want {
			t.Fatalf("keep: %v want %s", got, tc.want)
		}
	}
}

func TestAnalysisOptionsAndWindowBudgets(t *testing.T) {
	for _, o := range []SilenceOptions{{ThresholdDB: math.NaN()}, {ThresholdDB: 1}, {MinSilenceMS: -1}, {KeepAfterMS: -1}, {KeepBeforeMS: 5001}, {MinClipMS: 60001}, {AudioTrack: new(-1)}} {
		if _, err := silenceOptions(o); mediaerror.LocalErrorCode(err) != "invalid_argument" {
			t.Fatal(o, err)
		}
	}
	for _, o := range []VideoAnalysisOptions{{SceneThreshold: math.Inf(1)}, {SceneThreshold: -1}, {BlackPixelThreshold: 2}, {BlackPictureRatio: -1}, {MinBlackMS: -1}} {
		if _, err := videoAnalysisOptions(o); mediaerror.LocalErrorCode(err) != "invalid_argument" {
			t.Fatal(o, err)
		}
	}
	for _, interval := range [][3]int64{{-1, 100, 1000}, {100, 100, 1000}, {0, 0, AnalysisMaxWindowMS + 1}, {0, 1001, 1000}} {
		if _, _, err := analysisWindow(interval[0], interval[1], interval[2]); err == nil {
			t.Fatal("invalid window accepted", interval)
		}
	}
}

func TestAnalysisVideoStreamingParserIndependentBoundaries(t *testing.T) {
	o, _ := videoAnalysisOptions(VideoAnalysisOptions{MinBlackMS: 100})
	input := "frame:0 pts:0 pts_time:0\nlavfi.scene_score=0\nlavfi.black_start=0\nframe:1 pts:1 pts_time:0.2\nlavfi.scene_score=0.9\nlavfi.black_end=0.2\nframe:2 pts:2 pts_time:0.6\nlavfi.scene_score=0.8\nlavfi.black_start=0.6\nframe:3 pts:3 pts_time:0.9\nlavfi.scene_score=0\n"
	for _, chunk := range []int{1, 7, 65, 10000} {
		p := newVideoAnalysisParser(1000, 2000, 2, o)
		for data := []byte(input); len(data) > 0; {
			n := min(chunk, len(data))
			if _, err := p.Write(data[:n]); err != nil {
				t.Fatal(err)
			}
			data = data[n:]
		}
		if err := p.finish(); err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(p.result.Black) != "[{1000 1200} {1600 1900}]" || fmt.Sprint(p.result.Scenes) != "[{1200 0.9} {1600 0.8}]" || p.result.AnalyzedEndMS != 1900 {
			t.Fatalf("chunk %d: %+v", chunk, p.result)
		}
	}
}

func TestAnalysisVideoParserRejectsMalformedAndOversizedStreams(t *testing.T) {
	o, _ := videoAnalysisOptions(VideoAnalysisOptions{})
	for _, input := range []string{
		"frame:0 pts:0 pts_time:NaN\n",
		"frame:0 pts:0 pts_time:0\nlavfi.scene_score=NaN\n",
		"frame:0 pts:0 pts_time:0\nlavfi.scene_score=1.1\n",
		"frame:1 pts:0 pts_time:0\n",
		"frame:0 pts:0 pts_time:0\nlavfi.scene_score=0\nframe:1 pts:1 pts_time:-1\n",
		"frame:0 pts:0 pts_time:0\nlavfi.black_end=0\nlavfi.scene_score=0\n",
		"frame:0 pts:0 pts_time:0\nlavfi.scene_score=0\nlavfi.scene_score=0\n",
		"frame:0 pts:0 pts_time:0\n",
		"frame:0 pts:0 pts_time:0\nlavfi.scene_score=0",
		strings.Repeat("x", analysisVideoMaxLine+1),
	} {
		p := newVideoAnalysisParser(0, 1000, 0, o)
		_, err := p.Write([]byte(input))
		if err == nil {
			err = p.finish()
		}
		if err == nil {
			t.Fatal("invalid metadata accepted", input)
		}
	}
	p := newVideoAnalysisParser(0, 1000, 0, o)
	p.bytes = analysisVideoMaxBytes
	if _, err := p.Write([]byte("x")); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("metadata byte cap lost", err)
	}
	p = newVideoAnalysisParser(0, 1000, 0, o)
	p.frames = analysisVideoMaxFrames
	if _, err := p.Write([]byte("frame:180000 pts:0 pts_time:0\n")); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("frame cap lost", err)
	}
	p = newVideoAnalysisParser(0, 1000, 0, o)
	p.result.Scenes = make([]SceneChange, AnalysisMaxEvents)
	_, _ = p.Write([]byte("frame:0 pts:0 pts_time:0.5\nlavfi.scene_score=1\n"))
	if err := p.finish(); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("event cap lost", err)
	}
}

type analysisFailWriter struct{ err error }

func (w analysisFailWriter) Write([]byte) (int, error) { return 0, w.err }

func TestAnalysisWriterRefusalCancelsAndRetainsCause(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	want := errors.New("sample budget exhausted")
	w := &analysisOutput{writer: analysisFailWriter{want}, cancel: cancel}
	if _, err := w.Write([]byte("x")); !errors.Is(err, want) || !errors.Is(ctx.Err(), context.Canceled) || !errors.Is(w.err, want) {
		t.Fatal("writer did not cancel owned process", err, ctx.Err(), w.err)
	}
}
