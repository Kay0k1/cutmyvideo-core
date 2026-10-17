package subtitles

import "testing"

func TestExtractedSourceParserPreservesLegacyTolerance(t *testing.T) {
	text := []byte("7\r\n0:00:01,0004 --> 0:00:02.0004 align:start\r\nsource text\r\n")
	cues, err := ParseSourceSRT(text)
	if err != nil || len(cues) != 1 || cues[0].StartMS != 1000 || cues[0].EndMS != 2000 || cues[0].Text != "source text" {
		t.Fatal("extracted source grammar changed", cues, err)
	}
	if _, err := suppliedSubtitleClock("0:00:01,0004", "srt"); err == nil {
		t.Fatal("source extraction tolerance leaked into supplied sidecars")
	}
	for _, bad := range []string{"00:00:NaN", "00:00:Inf", "721:00:00", "00:60:00", "00:00:-1"} {
		if _, err := ParseSourceTime(bad); err == nil {
			t.Fatal("invalid source timestamp accepted", bad)
		}
	}
	if cues, err := ParseSourceSRT(nil); err != nil || cues != nil {
		t.Fatal("empty extracted source lost nil semantics", cues, err)
	}
	if _, err := ParseSourceSRT([]byte("1\n00:00:02,000 --> 00:00:01,000\nbackwards")); err == nil {
		t.Fatal("backwards source cue accepted")
	}
}
