package localmedia

import (
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaqc"
)

func TestQCPacketListingStreamsAndBounds(t *testing.T) {
	input := "pts=1000|dts=0|duration=40|flags=K__\npts=1020|dts=-40|duration=40|flags=K_D\npts=N/A|dts=1040|duration=40|flags=___\n\npts=1080|dts=1080|duration=N/A|flags=___\npts=5000|dts=5000|duration=40|flags=___\n"
	for _, chunk := range []int{1, 5, 64, 4096} {
		p := &qcPackets{clock: mediaqc.Clock{Num: 1, Den: 1000, OriginMS: 0}, from: 1000, until: 2000}
		for data := []byte(input); len(data) > 0; {
			n := min(chunk, len(data))
			if _, err := p.Write(data[:n]); err != nil {
				t.Fatal(err)
			}
			data = data[n:]
		}
		if err := p.finish(); err != nil || fmt.Sprint(p.ticks) != "[1000 1040 1080]" || p.latest != 1080 || p.lastDuration != 0 {
			t.Fatalf("chunk %d: %+v %v", chunk, p.ticks, err)
		}
	}
	for _, bad := range []string{"pts=x|dts=0\n", "pts=N/A|dts=N/A\n", "garbage\n", strings.Repeat("x", qcPacketMaxLine+1)} {
		p := &qcPackets{clock: mediaqc.Clock{Num: 1, Den: 1000}, until: 1000}
		_, err := p.Write([]byte(bad))
		if err == nil {
			err = p.finish()
		}
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	p := &qcPackets{clock: mediaqc.Clock{Num: 1, Den: 1000}, until: 1000, ticks: make([]int64, analysisVideoMaxFrames)}
	if _, err := p.Write([]byte("pts=1|dts=1\n")); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("frame cap lost", err)
	}
	p = &qcPackets{bytes: analysisVideoMaxBytes}
	if _, err := p.Write([]byte("x")); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("listing byte cap lost", err)
	}
}

func TestQCPCMAndFreezeMetadata(t *testing.T) {
	var data []byte
	for _, v := range []float32{0, 0, 0.0005, -0.5, 1.5, 0.2, -1.25, 0, 0, 0} {
		data = binary.LittleEndian.AppendUint32(data, math.Float32bits(v))
	}
	p := &qcPCM{channels: 2, limit: 10, first: -1, last: -1}
	for _, b := range data {
		if _, err := p.Write([]byte{b}); err != nil {
			t.Fatal(err)
		}
	}
	if p.samples != 10 || p.clipped != 2 || p.peak != 1.5 || p.first != 1 || p.last != 3 || p.partialCount != 0 {
		t.Fatalf("pcm: %+v", p)
	}
	if _, err := p.Write(data[:4]); mediaerror.LocalErrorCode(err) != "output_limit" {
		t.Fatal("sample cap lost", err)
	}
	p = &qcPCM{channels: 1, limit: 10, first: -1, last: -1}
	if _, err := p.Write(binary.LittleEndian.AppendUint32(nil, math.Float32bits(float32(math.NaN())))); err == nil {
		t.Fatal("nonfinite sample accepted")
	}

	o, _ := videoAnalysisOptions(VideoAnalysisOptions{MinBlackMS: 1})
	parser := newVideoAnalysisParser(0, 2000, 0, o)
	freeze := &qcFreeze{parser: parser, open: -1}
	parser.skipScene, parser.other = true, freeze.metadata
	input := "frame:0 pts:0 pts_time:0\nqc.frame=1\nframe:1 pts:1 pts_time:0.6\nlavfi.freezedetect.freeze_start=0\nframe:2 pts:2 pts_time:1\nlavfi.freezedetect.freeze_end=1\nframe:3 pts:3 pts_time:1.2\nlavfi.freezedetect.freeze_start=1\nqc.frame=1\nframe:4 pts:4 pts_time:1.9\n"
	if _, err := parser.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	if err := parser.finish(); err != nil {
		t.Fatal(err)
	}
	if err := freeze.finish(parser.result.AnalyzedEndMS); err != nil || fmt.Sprint(freeze.events) != "[{0 1000} {1000 1900}]" || len(parser.result.Scenes) != 0 {
		t.Fatalf("freeze: %+v %v", freeze.events, err)
	}
	for _, bad := range []string{
		"frame:0 pts:0 pts_time:0\nlavfi.freezedetect.freeze_end=0\n",
		"frame:0 pts:0 pts_time:0\nlavfi.freezedetect.freeze_start=0.5\n",
		"frame:0 pts:0 pts_time:0.5\nlavfi.freezedetect.freeze_start=0\nlavfi.freezedetect.freeze_start=0\n",
	} {
		parser := newVideoAnalysisParser(0, 2000, 0, o)
		freeze := &qcFreeze{parser: parser, open: -1}
		parser.skipScene, parser.other = true, freeze.metadata
		if _, err := parser.Write([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
