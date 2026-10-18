package localmedia

import (
	"fmt"
	"strings"
	"testing"
)

func TestLoudnessCoveragePreservesNativeSourceClockAcrossWrites(t *testing.T) {
	for _, rate := range []int64{44100, 48000, 96000, 192000} {
		p := &loudnessCoverage{rate: rate, windowMS: 6000}
		data := fmt.Sprintf("frame:0    pts:%d    pts_time:5\nlavfi.astats.Overall.Number_of_samples=%d.000000\nframe:1    pts:%d    pts_time:5.5\nlavfi.astats.Overall.Number_of_samples=%d.000000\n", 5*rate, rate/2, 11*rate/2, rate/2)
		// Arbitrary pipe boundaries must preserve the delayed selected clock,
		// including its final frame duration rather than its packet start PTS.
		for i := range len(data) {
			if _, err := p.Write([]byte(data[i : i+1])); err != nil {
				t.Fatal(rate, err)
			}
		}
		if err := p.finish(); err != nil || p.endMS != 6000 || p.length != 0 || p.pending {
			t.Fatal(rate, p.endMS, err)
		}
	}
	p := &loudnessCoverage{rate: 48000, windowMS: 1000}
	if _, err := p.Write([]byte("frame:0 pts:47999 pts_time:0.999979\nlavfi.astats.Overall.Number_of_samples=1.000000\n")); err != nil || p.finish() != nil || p.endMS != 1000 {
		t.Fatal("fractional final sample clock was rounded or clamped incorrectly", p.endMS, err)
	}
	p = &loudnessCoverage{rate: 48000, windowMS: 24 * 3600000}
	if _, err := p.Write([]byte("frame:0 pts:4147198976 pts_time:86400\nlavfi.astats.Overall.Number_of_samples=1024.000000\n")); err != nil || p.finish() != nil || p.endMS != p.windowMS {
		t.Fatal("rounded diagnostic timestamp replaced the exact 24-hour sample clock", p.endMS, err)
	}
	for _, test := range []struct {
		pts, samples int64
		text         string
		want         int64
	}{{1905132345, 6789, "43200.3", 43200434}, {3810215000, 12345, "86399.4", 86399713}} {
		p = &loudnessCoverage{rate: 44100, windowMS: 24 * 3600000}
		data := fmt.Sprintf("frame:0 pts:%d pts_time:%s\nlavfi.astats.Overall.Number_of_samples=%d.000000\n", test.pts, test.text, test.samples)
		if _, err := p.Write([]byte(data)); err != nil || p.finish() != nil || p.endMS != test.want {
			t.Fatal("44.1 kHz source clock lost nonaligned samples in a long window", p.endMS, test.want, err)
		}
	}
}

func TestLoudnessCoverageRejectsIncompleteOrMalformedFrames(t *testing.T) {
	for _, data := range []string{
		"", "frame:0 pts:0 pts_time:0\n", "frame:0 pts:0 pts_time:0",
		"lavfi.astats.Overall.Number_of_samples=1024.000000\n",
		"frame:1 pts:0 pts_time:0\n", "frame:0 pts:0 pts_time:NaN\n",
		"frame:0 pts:unknown pts_time:0\n",
		"frame:0 pts:0 pts_time:-1\n", "frame:0 pts:48001 pts_time:1.00001\n",
		"frame:0 pts:0 pts_time:0\nframe:1 pts:0 pts_time:0\n",
		"frame:0 pts:0 pts_time:0\nlavfi.astats.Overall.Number_of_samples=NaN\n",
		"frame:0 pts:0 pts_time:0\nlavfi.astats.Overall.Number_of_samples=0.000000\n",
		"frame:0 pts:0 pts_time:0\nlavfi.astats.Overall.Number_of_samples=1.500000\n",
		"frame:0 pts:0 pts_time:0\nlavfi.astats.Overall.Number_of_samples=2147483648\n",
		"frame:0 pts:43200 pts_time:0.9\nlavfi.astats.Overall.Number_of_samples=48000.000000\n",
		"frame:0 pts:0 pts_time:0\nunsupported=1024\n", strings.Repeat("x", 257),
	} {
		p := &loudnessCoverage{rate: 48000, windowMS: 1000}
		_, err := p.Write([]byte(data))
		if err == nil {
			err = p.finish()
		}
		if err == nil {
			t.Fatal("malformed or incomplete frame metadata certified coverage", data)
		}
		if p.length > len(p.line) {
			t.Fatal("unbounded metadata line retained")
		}
	}
}
