package edit

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCutListCSVIndependentSegmentsAndMarkers(t *testing.T) {
	// The first record and open marker follow the documented external format;
	// sub-millisecond input is intentionally rounded by the millisecond contract.
	list, err := ParseCutList("csv", []byte(",56.9568,Первый\n70,842.33,\"Quoted, label\"\n1234,,Last marker\n"))
	if err != nil || len(list.Entries) != 3 || list.Entries[0].StartMS != 0 || *list.Entries[0].EndMS != 56957 || *list.Entries[1].EndMS != 842330 || list.Entries[2].EndMS != nil {
		t.Fatal(list, err)
	}
	if _, err := CutListRanges(list, 0); err == nil {
		t.Fatal("final marker must not disappear without a duration")
	}
	ranges, err := CutListRanges(list, 1240000)
	if err != nil || ranges[2] != (Range{StartMS: 1234000, EndMS: 1240000, Label: "Last marker"}) {
		t.Fatal(ranges, err)
	}
	encoded, err := EncodeCutList("csv", list)
	if err != nil || string(encoded) != "0,56.957,Первый\n70,842.330,\"Quoted, label\"\n1234,,Last marker\n" {
		t.Fatal(string(encoded), err)
	}
	if _, err := CutListRanges(list, 1000000); err == nil {
		t.Fatal("duration must cover all explicit intervals and markers")
	}
}

func TestCutListDecimalMillisecondsHaveNoFloatOrOctalDrift(t *testing.T) {
	for _, test := range []struct {
		input string
		ms    int64
	}{
		{"0", 0}, {"010", 10000}, {"0.0005", 1}, {"0.000499999", 0},
		{"1.2345", 1235}, {"9007199254740.991", 9007199254740991},
	} {
		ms, err := decimalSecondsMS(test.input)
		if err != nil || ms != test.ms {
			t.Fatalf("%q = %d, %v; want %d", test.input, ms, err, test.ms)
		}
	}
	for _, input := range []string{"NaN", "Inf", "-1", "+1", "1e3", " 1", "1 ", ".1", "1.", "1.0000000001", "9007199254740.992", "999999999999999999999999"} {
		if _, err := decimalSecondsMS(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
}

func TestCutListChapterIndependentClockAndEscapes(t *testing.T) {
	input := ";FFMETADATA1\nmajor_brand=isom\n[CHAPTER]\nTIMEBASE=1/90000\nSTART=45000\nEND=180000\ntitle=Глава\\=1\\; \\#тест\\\\путь\\\nline\n"
	list, err := ParseCutList("ffmetadata", []byte(input))
	if err != nil || len(list.Entries) != 1 || list.Entries[0].StartMS != 500 || *list.Entries[0].EndMS != 2000 || list.Entries[0].Label != "Глава=1; #тест\\путь\nline" {
		t.Fatal(list, err)
	}
	encoded, err := EncodeCutList("ffmetadata", list)
	if err != nil || !bytes.Contains(encoded, []byte("TIMEBASE=1/1000\nSTART=500\nEND=2000\n")) {
		t.Fatal(string(encoded), err)
	}
	again, err := ParseCutList("ffmetadata", encoded)
	if err != nil || !reflect.DeepEqual(again, list) {
		t.Fatal(again, list, err)
	}
}

func TestCutListOutputChaptersUseActualClock(t *testing.T) {
	list, err := CutListFromSegments([]Segment{
		{Index: 0, Label: "Later source first", SourceStartMS: 6000, SourceEndMS: 8500, OutputStartMS: 0, OutputEndMS: 2500},
		{Index: 1, Label: "Earlier second", SourceStartMS: 1500, SourceEndMS: 2500, OutputStartMS: 2500, OutputEndMS: 3500},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCutList("ffmetadata", list)
	if err != nil || !strings.Contains(string(encoded), "START=0\nEND=2500") || !strings.Contains(string(encoded), "START=2500\nEND=3500") || strings.Contains(string(encoded), "START=6000") {
		t.Fatal(string(encoded), err)
	}
	end := int64(3000)
	list.Entries[1].StartMS, list.Entries[1].EndMS = 1000, &end
	if _, err := EncodeCutList("ffmetadata", list); err == nil {
		t.Fatal("overlapping output chapters accepted")
	}
	if _, err := EncodeCutList("csv", list); err != nil {
		t.Fatal("CSV should retain deliberate overlapping source edits", err)
	}
}

func TestCutListChapterLineEndingsAndLiteralCarriageReturn(t *testing.T) {
	list, err := ParseCutList("ffmetadata", []byte(";FFMETADATA1\r\n[CHAPTER]\r\nTIMEBASE=1/1000\r\nSTART=0\r\nEND=1000\r\ntitle=Windows\r\n"))
	if err != nil || list.Entries[0].Label != "Windows" {
		t.Fatal(list, err)
	}
	list.Entries[0].Label = "literal\r"
	encoded, err := EncodeCutList("ffmetadata", list)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ParseCutList("ffmetadata", encoded)
	if err != nil || again.Entries[0].Label != "literal\r" {
		t.Fatal(again, string(encoded), err)
	}
}

func TestCutListRejectsInvalidOrExcessiveInput(t *testing.T) {
	invalidCSV := []string{"", "start,end,label\n", "0,1\n", "0,1,a,extra\n", "0,0,empty\n", "2,1,inverted\n", "-1,2,negative\n", "0,1,\"unclosed\n", "0,1," + strings.Repeat("x", 201), "0,1,\x00", "0,1,\xff"}
	for _, text := range invalidCSV {
		if _, err := ParseCutList("csv", []byte(text)); err == nil {
			t.Errorf("accepted invalid CSV %q", text)
		}
	}
	if _, err := ParseCutList("csv", bytes.Repeat([]byte{'a'}, MaxCutListBytes+1)); err == nil {
		t.Fatal("oversized document accepted")
	}
	if _, err := ParseCutList("csv", []byte(strings.Repeat("0,1,label\n", 129))); err == nil {
		t.Fatal("too many entries accepted")
	}
	for _, body := range []string{
		"[CHAPTER]\nSTART=0\nEND=2\n", "[STREAM]\nTIMEBASE=1/1\nSTART=0\nEND=1\n",
		"[CHAPTER]\nTIMEBASE=1/0\nSTART=0\nEND=1\n", "[CHAPTER]\nTIMEBASE=1/1000\nSTART=0\nSTART=1\nEND=2\n",
		"[CHAPTER]\nTIMEBASE=1/1000\nSTART=1\nEND=1\n", "[CHAPTER]\nTIMEBASE=1/1\nSTART=0\nEND=1\ntitle=unescaped#comment\n",
		"[CHAPTER]\nTIMEBASE=1000000001/1\nSTART=0\nEND=1\n", "[CHAPTER]\nTIMEBASE=1/1\nSTART=0\nEND=9223372036854775807\n",
	} {
		if _, err := ParseCutList("ffmetadata", []byte(";FFMETADATA1\n"+body)); err == nil {
			t.Errorf("accepted invalid chapter metadata %q", body)
		}
	}
	var doc strings.Builder
	doc.WriteString(";FFMETADATA1\n")
	for i := 0; i < 129; i++ {
		fmt.Fprintf(&doc, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\n", i*2, i*2+1)
	}
	if _, err := ParseCutList("ffmetadata", []byte(doc.String())); err == nil {
		t.Fatal("too many chapters accepted")
	}
}
