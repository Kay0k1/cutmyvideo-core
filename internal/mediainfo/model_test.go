package mediainfo

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func TestInfoValidationPreservesProbeBoundaries(t *testing.T) {
	const fields = "source stream metadata exceeds the field limit"
	const tags = "source track tags exceed the field limit"
	cases := []struct {
		name   string
		change func(*Info)
		want   string
	}{
		{"empty", func(p *Info) { p.Streams = nil }, "source contains no supported streams"},
		{"8K", func(p *Info) { p.Streams[0].Width, p.Streams[0].Height = 7680, 4320 }, ""},
		{"pixel boundary", func(p *Info) { p.Streams[0].Width, p.Streams[0].Height = MaxPixels/2, 2 }, ""},
		{"pixel overflow", func(p *Info) { p.Streams[0].Width, p.Streams[0].Height = MaxPixels/2+1, 2 }, "source video dimensions exceed the decoder limit"},
		{"zero height", func(p *Info) { p.Streams[0].Height = 0 }, "source video dimensions exceed the decoder limit"},
		{"negative index", func(p *Info) { p.Streams[0].Index = -1 }, "source contains invalid stream indices"},
		{"duplicate index", func(p *Info) { p.Streams = append(p.Streams, p.Streams[0]) }, "source contains invalid stream indices"},
		{"stream boundary", func(p *Info) {
			p.Streams = make([]Stream, 32)
			for i := range p.Streams {
				p.Streams[i] = Stream{Index: i, CodecType: "audio"}
			}
		}, ""},
		{"stream overflow", func(p *Info) { p.Streams = make([]Stream, 33) }, "source contains no supported streams"},
		{"metadata boundary", func(p *Info) { p.Streams[0].CodecName = strings.Repeat("x", 64) }, ""},
		{"metadata overflow", func(p *Info) { p.Streams[0].CodecName = strings.Repeat("x", 65) }, fields},
		{"language boundary", func(p *Info) { p.Streams[0].Tags.Language = strings.Repeat("x", 128) }, ""},
		{"language overflow", func(p *Info) { p.Streams[0].Tags.Language = strings.Repeat("x", 129) }, tags},
		{"title byte boundary", func(p *Info) { p.Streams[0].Tags.Title = "ab" + strings.Repeat("界", 170) }, ""},
		{"title byte overflow", func(p *Info) { p.Streams[0].Tags.Title = "abc" + strings.Repeat("界", 170) }, tags},
		{"hash boundary", func(p *Info) { p.Streams[0].ExtradataHash = strings.Repeat("x", 80) }, ""},
		{"hash overflow", func(p *Info) { p.Streams[0].ExtradataHash = strings.Repeat("x", 81) }, fields},
		{"side data boundary", func(p *Info) { p.Streams[0].SideDataList = make([]SideData, 32) }, ""},
		{"side data overflow", func(p *Info) { p.Streams[0].SideDataList = make([]SideData, 33) }, fields},
		{"NaN rotation", func(p *Info) { n := math.NaN(); p.Streams[0].SideDataList = []SideData{{Rotation: &n}} }, "source has invalid display rotation"},
		{"infinite rotation", func(p *Info) { n := math.Inf(1); p.Streams[0].SideDataList = []SideData{{Rotation: &n}} }, "source has invalid display rotation"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Info{Streams: []Stream{{Index: 0, CodecType: "video", CodecName: "h264", Width: 160, Height: 90}}}
			tc.change(&p)
			err := p.ValidateStreams()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid bounded metadata rejected: %v", err)
				}
			} else if err == nil || err.Error() != tc.want {
				t.Fatalf("validation error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestInfoDurationAndSelectionPreserveSourceOrder(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
		ok   bool
	}{
		{"1.5", 1500, true}, {"0.0006", 1, true}, {"2592000", 2592000000, true},
		{"", 0, false}, {"0", 0, false}, {"-1", 0, false}, {"NaN", 0, false}, {"+Inf", 0, false}, {"2592000.001", 0, false},
	} {
		p := Info{}
		p.Format.Duration = tc.raw
		got, err := p.DurationMS()
		if got != tc.want || (err == nil) != tc.ok || (err != nil && err.Error() != "source has no supported finite duration") {
			t.Fatalf("duration %q: got=%d err=%v, want=%d valid=%v", tc.raw, got, err, tc.want, tc.ok)
		}
	}
	const raw = `{"format":{"duration":"1.5","start_time":"0.5"},"streams":[
		{"index":8,"codec_type":"audio","codec_name":"aac","tags":{"language":" rus ","title":" Alt "}},
		{"index":0,"codec_type":"video","width":160,"height":90,"disposition":{"attached_pic":1}},
		{"index":7,"codec_type":"audio","disposition":{"default":1}},
		{"index":3,"codec_type":"audio","disposition":{"default":1}},
		{"index":9,"codec_type":"video","width":160,"height":90},
		{"index":2,"codec_type":"video","width":320,"height":180,"side_data_list":[{}, {"rotation":0}, {"rotation":90}]}
	],"library_versions":[{"name":"libavformat","major":62}]}`
	var p Info
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	video, audio := p.SelectedVideo(), p.SelectedAudio()
	if video != &p.Streams[5] || audio != &p.Streams[3] || video.Rotation() != 0 {
		t.Fatalf("selection lost absolute-index/default/first-rotation policy: video=%+v audio=%+v", video, audio)
	}
	tracks := p.Tracks()
	want := []edit.Track{
		{Index: 8, Type: "audio", Codec: "aac", Language: "rus", Title: "Alt"},
		{Index: 0, Type: "video", AttachedPicture: true},
		{Index: 7, Type: "audio", Default: true}, {Index: 3, Type: "audio", Default: true},
		{Index: 9, Type: "video"}, {Index: 2, Type: "video"},
	}
	if !reflect.DeepEqual(tracks, want) || len(p.LibraryVersions) != 1 || p.LibraryVersions[0].Name != "libavformat" || p.LibraryVersions[0].Major != 62 {
		t.Fatalf("track projection or probe library metadata changed: tracks=%+v libraries=%+v", tracks, p.LibraryVersions)
	}
	after, err := json.Marshal(p)
	if err != nil || string(after) != string(before) {
		t.Fatalf("selection mutated source probe metadata: %v", err)
	}
	video.Width = 640
	if p.Streams[5].Width != 640 {
		t.Fatal("selected video no longer points into the original stream slice")
	}
}
