package mediaqc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestMeasureTimingClassifiesCadence(t *testing.T) {
	clock := Clock{Num: 1, Den: 15360}
	var cfr []int64
	for i := int64(89); i >= 0; i-- { // decode order does not matter
		cfr = append(cfr, i*512)
	}
	var v Video
	MeasureTiming(&v, cfr, clock, 512, 3000, 30)
	if v.Timing != "cfr" || v.FrameCount != 90 || *v.FPS != 30 || v.ExpectedFrames != 90 || v.EndMS != 3000 || v.DurationMS != 3000 || v.GapCount != 0 || v.IntervalMS.Median != 33.333 {
		t.Fatalf("cfr: %+v", v)
	}

	var dropped []int64
	for i := int64(0); i < 90; i++ {
		if i < 40 || i > 54 {
			dropped = append(dropped, i*512)
		}
	}
	v = Video{}
	MeasureTiming(&v, dropped, clock, 512, 3000, 30)
	if v.Timing != "vfr" || v.FrameCount != 75 || v.ExpectedFrames != 90 || v.GapCount != 1 || v.MissingFrames != 15 || fmt.Sprint(v.Gaps) != "[{1333 15}]" || *v.FPS != 30 {
		t.Fatalf("drop: %+v", v)
	}

	// A one millisecond time base rounds 30 fps to 33/34 ms; that is regular.
	var coarse []int64
	for i := 0; i < 30; i++ {
		coarse = append(coarse, int64(math.Round(float64(i)*1000/30)))
	}
	v = Video{}
	MeasureTiming(&v, append(coarse, coarse[29]), Clock{Num: 1, Den: 1000, OriginMS: 0}, 0, 1000, 30)
	if v.Timing != "vfr" || v.DuplicateCount != 1 || fmt.Sprint(v.Duplicates) != "[967]" || math.Abs(*v.FPS-30) > 0.02 {
		t.Fatalf("duplicate: %+v", v)
	}
	v = Video{}
	MeasureTiming(&v, coarse, Clock{Num: 1, Den: 1000, OriginMS: 0}, 0, 1000, 30)
	if v.Timing != "cfr" || math.Abs(*v.FPS-30) > 0.01 || v.EndMS != 1000 {
		t.Fatalf("coarse: %+v %v", v, *v.FPS)
	}

	v = Video{}
	MeasureTiming(&v, []int64{2000}, Clock{Num: 1, Den: 1000, OriginMS: 1000}, 40, 5000, 25)
	if v.Timing != "unknown" || v.FPS != nil || v.FirstFrameMS != 1000 || v.EndMS != 1040 || v.Gaps == nil || v.Duplicates == nil {
		t.Fatalf("single frame: %+v", v)
	}
	if WithoutBlack([]Interval{{0, 500}, {990, 2010}}, []Interval{{1000, 2000}}, 34)[0] != (Interval{0, 500}) {
		t.Fatal("black interval reported again as freeze")
	}
	if ParseRate("30000/1001") < 29.97 || ParseRate("0/0") != 0 || ParseRate("30") != 0 {
		t.Fatal("rate parsing")
	}
}

func box(kind string, length int) []byte {
	b := binary.BigEndian.AppendUint32(nil, uint32(length))
	return append(append(b, kind...), make([]byte, length-8)...)
}

func TestFastStartReadsTopLevelBoxes(t *testing.T) {
	large := append(binary.BigEndian.AppendUint32(nil, 1), "mdat"...)
	large = append(binary.BigEndian.AppendUint64(large, 24), make([]byte, 8)...)
	for _, test := range []struct {
		name string
		file []byte
		want bool
		err  bool
	}{
		{"faststart", bytes.Join([][]byte{box("ftyp", 16), box("moov", 40), box("mdat", 100)}, nil), true, false},
		{"moov-last", bytes.Join([][]byte{box("ftyp", 16), box("free", 8), box("mdat", 100), box("moov", 40)}, nil), false, false},
		{"large-mdat-first", append(large, box("moov", 16)...), false, false},
		{"fragmented", bytes.Join([][]byte{box("ftyp", 16), box("moov", 40), box("moof", 20)}, nil), true, false},
		{"to-end", append(box("moov", 16), append(binary.BigEndian.AppendUint32(nil, 0), "mdat"...)...), true, false},
		{"no-moov", box("mdat", 100), false, true},
		{"overflow", append(box("ftyp", 16), append(binary.BigEndian.AppendUint32(nil, 4096), "moov"...)...), false, true},
		{"short", append(box("moov", 16), 0, 0, 0), false, true},
	} {
		got, err := FastStart(bytes.NewReader(test.file), int64(len(test.file)))
		if got != test.want || (err != nil) != test.err {
			t.Errorf("%s: got %v, %v", test.name, got, err)
		}
	}
}

func TestExpectationsParseAndEvaluate(t *testing.T) {
	for _, bad := range [][]string{{"fps"}, {"fps=abc"}, {"fps=0"}, {"cfr=1"}, {"size=1080"}, {"size=0x10"}, {"loud=1"}, {"fps=30", "fps=60"},
		{"audio", "no-audio"}, {"true-peak=-1±1"}, {"duration=5±-1"}, {"lufs=5"}, {""}, {"duration=NaN"}} {
		if _, err := ParseExpectations(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	list, err := ParseExpectations([]string{"fps=30", "size=320x240", "duration=3±0.1", "duration2=1"}[:3])
	if err != nil || len(list) != 3 {
		t.Fatal(list, err)
	}
	fps, peak, lufs := 29.97, -0.5, -14.2
	fast := true
	r := Report{Container: Container{DurationMS: 3050, FastStart: &fast}, Video: &Video{Width: 320, Height: 240, FPS: &fps, Timing: "cfr"}, Audio: &Audio{TruePeakDBTP: &peak, IntegratedLUFS: &lufs, ClippedSamples: 3}}
	all, err := ParseExpectations(strings.Split("fps=29.97,size=320x240,duration=3+-0.1,true-peak=-1,lufs=-14,cfr,no-drops,no-black,no-freeze,no-clipping,faststart,audio,video", ","))
	if err != nil {
		t.Fatal(err)
	}
	checks := Evaluate(&r, all)
	var failed []string
	for _, c := range checks {
		if c.Status == "fail" {
			failed = append(failed, c.Name)
		}
	}
	if fmt.Sprint(failed) != "[true-peak no-clipping]" || Passed(checks) || !Passed(nil) {
		t.Fatalf("checks %+v", checks)
	}
	missing, _ := ParseExpectations([]string{"true-peak=-1", "fps=30", "no-audio", "faststart"})
	checks = Evaluate(&Report{}, missing)
	for i, want := range []string{"fail", "fail", "pass", "fail"} {
		if checks[i].Status != want || want == "fail" && checks[i].Message == "" {
			t.Fatalf("missing data: %+v", checks)
		}
	}
	r.Video.Timing = "vfr"
	if c := Evaluate(&r, all[:1])[0]; c.Status != "fail" || !strings.Contains(c.Message, "not constant") {
		t.Fatal("fps accepted variable timing", c)
	}
}
