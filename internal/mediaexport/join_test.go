package mediaexport

import (
	"context"
	"errors"
	"path"
	"reflect"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

type joinedFixture struct {
	*exportRuntimeFixture
	writes                                                               map[string]string
	writeOrder                                                           []string
	runs                                                                 [][]string
	resolves                                                             []string
	workspaceErr, writeErr, resolveErr, muxErr, verifyErr, capabilityErr error
	removeErr                                                            error
	resolveBad                                                           string
	pinned, changedFile, capability                                      bool
	sourceErr                                                            error
	fileStats                                                            map[string]int
	source                                                               mediainfo.Info
	muxSegments                                                          []edit.Segment
	muxMedia, muxOut                                                     string
	verifiedSelection                                                    Selection
	verifiedExpected                                                     int64
	copyDuration, finalDuration                                          int64
}

func newJoinedFixture() *joinedFixture {
	f := newExportRuntimeFixture()
	return &joinedFixture{exportRuntimeFixture: f, writes: make(map[string]string), fileStats: make(map[string]int), source: f.output, copyDuration: 1200, finalDuration: 3600}
}
func (f *joinedFixture) JoinPath(parts ...string) string { return path.Join(parts...) }
func (f *joinedFixture) BaseDir(name string) string      { return path.Dir(name) }
func (f *joinedFixture) ResolveLocalPath(name string) (string, error) {
	f.events = append(f.events, "resolve:"+name)
	f.resolves = append(f.resolves, name)
	if name == f.resolveBad && f.resolveErr != nil {
		return "", f.resolveErr
	}
	if strings.HasPrefix(name, "/") {
		return name, nil
	}
	return "/resolved/" + name, nil
}
func (f *joinedFixture) NewWorkspace(string, string) (string, error) {
	f.events = append(f.events, "workspace")
	return "/workspace/joined", f.workspaceErr
}
func (f *joinedFixture) RemoveAll(name string) error {
	f.events = append(f.events, "cleanup:"+name)
	return f.removeErr
}
func (f *joinedFixture) Remove(name string) error {
	f.events = append(f.events, "remove:"+path.Base(name))
	return f.removeErr
}
func (f *joinedFixture) Rename(a, b string) error {
	f.events = append(f.events, "rename:"+path.Base(a)+":"+path.Base(b))
	return nil
}
func (f *joinedFixture) WritePlan(name string, data []byte) error {
	f.events = append(f.events, "write:"+path.Base(name))
	f.writeOrder = append(f.writeOrder, name)
	f.writes[name] = string(data)
	return f.writeErr
}

type joinedFileFixture struct {
	size     int64
	identity string
}

func (f *joinedFileFixture) Size() int64 { return f.size }
func (f *joinedFileFixture) Same(other JoinedFile) bool {
	v, ok := other.(*joinedFileFixture)
	return ok && f.size == v.size && f.identity == v.identity
}
func (f *joinedFixture) StatFile(name string) (JoinedFile, error) {
	f.events = append(f.events, "file:"+path.Base(name))
	f.fileStats[name]++
	id := name
	if f.changedFile && f.fileStats[name] > 1 {
		id += " changed"
	}
	return &joinedFileFixture{size: 1024, identity: id}, nil
}

type joinedFenceFixture struct{ owner *joinedFixture }

func (f *joinedFenceFixture) Check() error {
	f.owner.events = append(f.owner.events, "fence")
	return f.owner.sourceErr
}
func (f *joinedFixture) PinnedSource(_ context.Context, input Input) SourceFence {
	f.events = append(f.events, "pin:"+input.Path)
	if !f.pinned || input.Remote {
		return nil
	}
	return &joinedFenceFixture{owner: f}
}
func (f *joinedFixture) ProbeStreams(context.Context, string, bool) (mediainfo.Info, error) {
	f.events = append(f.events, "streams")
	return f.source, nil
}
func (f *joinedFixture) MuxSubtitles(_ context.Context, _ string, _ mediainfo.Info, media, out string, segments []edit.Segment, _ edit.ExportRequest, _ int64) error {
	f.events = append(f.events, "mux")
	f.muxMedia, f.muxOut = media, out
	f.muxSegments = segments
	return f.muxErr
}
func (f *joinedFixture) VerifyAccurate(_ context.Context, _ string, _ edit.ExportRequest, expected, _ int64, selection Selection) (mediainfo.Info, error) {
	f.events = append(f.events, "verify-accurate")
	f.verifiedExpected, f.verifiedSelection = expected, selection
	return f.output, f.verifyErr
}
func (f *joinedFixture) SupportsAudioPacketDrop(context.Context) (bool, error) {
	f.events = append(f.events, "capability")
	return f.capability, f.capabilityErr
}
func (f *joinedFixture) Run(ctx context.Context, args []string, total int64, report func(int64)) error {
	f.runs = append(f.runs, append([]string(nil), args...))
	return f.exportRuntimeFixture.Run(ctx, args, total, report)
}
func (f *joinedFixture) NearestKeyframe(ctx context.Context, name string, start int64, remote bool, index int) (Keyframe, error) {
	f.keyframe = Keyframe{PTS: start - 200, DTS: start - 280, HasDTS: true}
	return f.exportRuntimeFixture.NearestKeyframe(ctx, name, start, remote, index)
}
func (f *joinedFixture) Probe(ctx context.Context, name string, remote bool) (mediainfo.Info, int64, error) {
	info, duration, err := f.exportRuntimeFixture.Probe(ctx, name, remote)
	if strings.HasPrefix(path.Base(name), "video-") {
		duration = f.copyDuration
	} else if name == "final.mp4" {
		duration = f.finalDuration
	}
	return info, duration, err
}
func joinedEventCount(events []string, name string) int {
	n := 0
	for _, event := range events {
		if event == name {
			n++
		}
	}
	return n
}
func joinedEventIndex(events []string, name string) int {
	for i, event := range events {
		if event == name {
			return i
		}
	}
	return -1
}

func TestJoinedExportPreservesAdmissionOrderSubtitlesAndCleanup(t *testing.T) {
	ranges := []edit.Range{{StartMS: 1000, EndMS: 2000, Label: "later"}, {EndMS: 500, Label: "earlier"}}
	inputs := [][]Input{{{Path: "source"}}, {{Path: "source"}}}
	for _, tc := range []struct {
		name   string
		change func(*joinedFixture, *edit.ExportRequest)
		want   string
		events []string
	}{
		{"crop before workspace", func(_ *joinedFixture, r *edit.ExportRequest) { r.Crop = &edit.Crop{Width: -1, Height: 1} }, "crop_invalid", nil},
		{"workspace error", func(f *joinedFixture, _ *edit.ExportRequest) { f.workspaceErr = errors.New("workspace cause") }, "workspace cause", []string{"workspace"}},
		{"selection error cleans workspace", func(f *joinedFixture, _ *edit.ExportRequest) { f.selectErr = errors.New("selection cause") }, "selection cause", []string{"workspace", "select", "cleanup:/workspace/joined"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := newJoinedFixture(), exportFixtureRequest()
			tc.change(f, &r)
			_, err := ExportJoined(context.Background(), exportFixtureConfig(), inputs, ranges, r, "final.mp4", nil, f)
			var p *mediainfo.Problem
			got := ""
			if errors.As(err, &p) {
				got = p.Code
			} else if err != nil {
				got = err.Error()
			}
			if got != tc.want || !reflect.DeepEqual(f.events, tc.events) {
				t.Fatalf("error/effect precedence changed: %v %v", err, f.events)
			}
		})
	}
	f, r := newJoinedFixture(), exportFixtureRequest()
	r.SubtitleMode = "preserve"
	f.source.Streams = append(append([]mediainfo.Stream(nil), f.source.Streams...), mediainfo.Stream{Index: 20, CodecType: "subtitle", CodecName: "subrip"})
	f.removeErr = errors.New("ignored deferred cleanup")
	segments, err := ExportJoined(context.Background(), exportFixtureConfig(), inputs, ranges, r, "final.mp4", nil, f)
	want := []edit.Segment{{Index: 0, Label: "later", SourceStartMS: 1000, SourceEndMS: 2000, OutputEndMS: 1000}, {Index: 1, Label: "earlier", SourceEndMS: 500, OutputStartMS: 1000, OutputEndMS: 1500}}
	if err != nil || !reflect.DeepEqual(segments, want) || !reflect.DeepEqual(f.muxSegments, want) || f.muxMedia != "/workspace/joined/media.mp4" || f.muxOut != "final.mp4" {
		t.Fatalf("preserved subtitle timeline changed: %+v %v", segments, err)
	}
	if exportArgsContain(f.runs[0], "-movflags", "+faststart") || joinedEventIndex(f.events, "mux") < joinedEventIndex(f.events, "verify-accurate") || f.events[len(f.events)-1] != "cleanup:/workspace/joined" {
		t.Fatal("mux/finalization/cleanup order changed", f.events)
	}
	f = newJoinedFixture()
	f.workspaceErr = errors.New("workspace wins")
	_, err = ExportJoined(context.Background(), exportFixtureConfig(), inputs, []edit.Range{{EndMS: 0}, ranges[1]}, exportFixtureRequest(), "final.mp4", nil, f)
	if err != f.workspaceErr || !reflect.DeepEqual(f.events, []string{"workspace"}) {
		t.Fatal("per-range validation moved before workspace creation", err, f.events)
	}
	f = newJoinedFixture()
	r = exportFixtureRequest()
	r.SubtitleMode = "preserve"
	remote := [][]Input{{{Path: "guarded", Remote: true}}, {{Path: "guarded", Remote: true}}}
	_, err = ExportJoined(context.Background(), exportFixtureConfig(), remote, ranges, r, "final.mp4", nil, f)
	var problem *mediainfo.Problem
	if !errors.As(err, &problem) || problem.Code != "subtitle_unavailable" || len(f.runs) != 0 || joinedEventCount(f.events, "streams") != 0 {
		t.Fatal("remote subtitle guard performed media work", err, f.events)
	}
}

func TestJoinedCopyPreservesMeasuredTimelineReuseAndBudget(t *testing.T) {
	ranges := []edit.Range{{StartMS: 1000, EndMS: 2000, Label: "first"}, {StartMS: 3000, EndMS: 4000, Label: "middle"}, {StartMS: 1000, EndMS: 2000, Label: "repeat"}}
	inputs := [][]Input{{{Path: "source"}}, {{Path: "source"}}, {{Path: "source"}}}
	for _, tc := range []struct {
		name    string
		limit   int64
		changed bool
		source  error
		want    string
	}{
		{"reused measured clocks", 3073, false, nil, ""}, {"logical repeat counts toward aggregate ceiling", 3072, false, nil, "limit"}, {"changed copied file", 3073, true, nil, "source_changed"}, {"changed original source", 3073, false, errors.New("pinned source changed"), "source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := newJoinedFixture(), exportFixtureRequest()
			f.pinned, f.changedFile, f.sourceErr = true, tc.changed, tc.source
			f.selection.Audio = nil
			f.output.Streams = f.output.Streams[:1]
			r.CutMode, r.NoAudio = "copy", true
			result, err := JoinCopy(context.Background(), exportFixtureConfig(), inputs, ranges, r, "final.mp4", "/workspace", tc.limit, nil, true, f)
			var p *mediainfo.Problem
			switch tc.want {
			case "":
				want := []edit.Segment{{Index: 0, Label: "first", SourceStartMS: 800, SourceEndMS: 2000, OutputEndMS: 1200}, {Index: 1, Label: "middle", SourceStartMS: 2800, SourceEndMS: 4000, OutputStartMS: 1200, OutputEndMS: 2400}, {Index: 2, Label: "repeat", SourceStartMS: 800, SourceEndMS: 2000, OutputStartMS: 2400, OutputEndMS: 3600}}
				if err != nil || !reflect.DeepEqual(result, want) || len(f.runs) != 3 || f.runs[2][len(f.runs[2])-1] != "final.mp4" {
					t.Fatalf("copy reuse/timeline changed: %+v %v runs=%d", result, err, len(f.runs))
				}
				plan := f.writes["/workspace/copy.ffconcat"]
				if strings.Count(plan, "file 'segment-0.mp4'") != 2 || strings.Count(plan, "duration 1.200") != 3 {
					t.Fatal("copy plan lost repeated actual intervals", plan)
				}
				faststarts := 0
				for _, args := range f.runs {
					if exportArgsContain(args, "-movflags", "+faststart") {
						faststarts++
					}
				}
				if faststarts != 1 {
					t.Fatal("copy intermediates performed final-only faststart", faststarts)
				}
			case "limit":
				if err != ErrOutputLimit || result != nil || len(f.runs) != 2 {
					t.Fatal("aggregate ignored reused bytes", err, f.events)
				}
			case "source_changed":
				if !errors.As(err, &p) || p.Code != "source_changed" || len(f.runs) != 2 {
					t.Fatal("changed copied file reused", err, f.events)
				}
			case "source":
				if err != tc.source || len(f.runs) != 2 {
					t.Fatal("source fence error was replaced", err, f.events)
				}
			}
			if joinedEventCount(f.events, "fence") != 1 {
				t.Fatal("reuse omitted or repeated its pinned source fence", f.events)
			}
		})
	}
}

func TestJoinedAccuratePreservesPlanRestrictionsAndVerification(t *testing.T) {
	ranges := []edit.Range{{StartMS: 11000, EndMS: 12000, Label: "later"}, {StartMS: 10000, EndMS: 10500, Label: "repeat"}}
	inputs := [][]Input{{{Path: "O'Brien.mp4", OffsetMS: 10000}, {Path: "https://guarded/audio", Remote: true, OffsetMS: 9000}}, {{Path: "O'Brien.mp4", OffsetMS: 10000}, {Path: "https://guarded/audio", Remote: true, OffsetMS: 9000}}}
	f, r := newJoinedFixture(), exportFixtureRequest()
	f.selection.AudioInput, f.selection.MP3DurationMode = 1, 2
	config := exportFixtureConfig()
	config.FFmpegProfile, config.FFmpegThreads = "compact", 2
	result, err := JoinAccurate(CollectionRenderContext(context.Background(), "draft"), config, inputs, ranges, r, "final.mp4", "/workspace", 1<<20, f.selection, func(int64) {}, false, f)
	if err != nil || len(result) != 2 || result[0].SourceStartMS != 11000 || result[1].OutputStartMS != 1000 || result[1].OutputEndMS != 1500 || f.verifiedExpected != 1500 || f.verifiedSelection.MP3DurationMode != 2 {
		t.Fatalf("accurate timeline/provenance changed: %+v %v", result, err)
	}
	local, remote := f.writes["/workspace/input-0.ffconcat"], f.writes["/workspace/input-1.ffconcat"]
	if !strings.Contains(local, "file '/resolved/O'\\''Brien.mp4'") || !strings.Contains(local, "inpoint 1.000\noutpoint 2.000\nduration 1.000") || !strings.Contains(local, "option protocol_whitelist file\noption format_whitelist "+InputFormats) {
		t.Fatal("local concat escaping/offset/restrictions changed", local)
	}
	if strings.Count(remote, "option protocol_whitelist http,tcp\noption format_whitelist "+InputFormats) != 2 || strings.Contains(remote, "option protocol_whitelist file") || strings.Contains(remote, "option format_whitelist "+InputFormats+",concat") {
		t.Fatal("untrusted remote child inherited trusted parent access", remote)
	}
	if !reflect.DeepEqual(exportArgValues(f.args, "-protocol_whitelist"), []string{"file", "file,http,tcp"}) || !reflect.DeepEqual(exportArgValues(f.args, "-threads"), []string{"1", "2", "2"}) || !exportArgsContain(f.args, "-preset", "ultrafast", "-crf", "28") || exportArgsContain(f.args, "-movflags", "+faststart") {
		t.Fatal("parent protocols/decoder budget/draft/finalization changed", f.args)
	}
	raw := errors.New("raw accurate process cause")
	f = newJoinedFixture()
	f.runErr, f.size = raw, 1<<30
	_, err = JoinAccurate(context.Background(), config, inputs, ranges, r, "final.mp4", "/workspace", 100, f.selection, nil, true, f)
	if err != raw || joinedEventCount(f.events, "stat") != 0 || joinedEventCount(f.events, "copy") != 0 || joinedEventCount(f.events, "rendered") != 0 || joinedEventCount(f.events, "verify-accurate") != 0 {
		t.Fatal("accurate failure acquired single-export error normalization", err, f.events)
	}
	f = newJoinedFixture()
	f.writeErr = errors.New("column write wins")
	f.resolveBad, f.resolveErr = "later-bad", errors.New("later path cause")
	changed := [][]Input{{{Path: "first"}, {Path: "later-bad"}}, {{Path: "second"}, {Path: "later-bad"}}}
	_, err = JoinAccurate(context.Background(), config, changed, []edit.Range{{EndMS: 1000}, {StartMS: 1000, EndMS: 2000}}, r, "final.mp4", "/workspace", 1<<20, f.selection, nil, true, f)
	if err != f.writeErr || !reflect.DeepEqual(f.resolves, []string{"first", "second"}) || len(f.writeOrder) != 1 {
		t.Fatal("planning resolved later columns before writing earlier ones", err, f.events)
	}
}

func TestJoinedCopyAudioPreservesCapabilityFallbackAndStages(t *testing.T) {
	for _, tc := range []struct {
		name, timebase string
		available      bool
		wantRuns       int
		wantCapability bool
	}{
		{"proven direct path", "1/1000", true, 1, true}, {"capability fallback", "1/1000", false, 2, true}, {"fine source clock retains staging", "1/48000", true, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newJoinedFixture()
			f.capability = tc.available
			f.selection.Audio.TimeBase, f.selection.Audio.StartTime = tc.timebase, "0"
			r := exportFixtureRequest()
			r.Format, r.CutMode = "mkv", "copy"
			err := CopyAudio(context.Background(), exportFixtureConfig(), []Input{{Path: "https://guarded/source", Remote: true, OffsetMS: 1000}}, f.selection, 2000, 4000, "/workspace/video.mkv", "final.mkv", r, 4096, f)
			if err != nil || len(f.runs) != tc.wantRuns || (joinedEventCount(f.events, "capability") > 0) != tc.wantCapability {
				t.Fatal("copy audio path selection changed", err, f.events)
			}
			if tc.wantRuns == 1 {
				if !exportArgsContain(f.runs[0], "-bsf:a", "noise=amount=0:drop=lt(pts\\,0)*not(eq(pts\\,nopts))") || !exportArgsContain(f.runs[0], "-itsoffset", "-1.000", "-seek_timestamp", "1", "-ss", "1.000") {
					t.Fatal("direct audio packet clocks changed", f.runs[0])
				}
			} else if joinedEventCount(f.events, "remove:audio.mka") != 1 || !reflect.DeepEqual(exportArgValues(f.runs[1], "-protocol_whitelist"), []string{"file", "file"}) {
				t.Fatal("staged audio cleanup or trusted remux inputs changed", f.events)
			}
		})
	}
	f := newJoinedFixture()
	f.selection.Audio.TimeBase, f.selection.Audio.StartTime = "1/1000", "0"
	f.capabilityErr = errors.New("capability cause")
	r := exportFixtureRequest()
	r.Format = "mkv"
	err := CopyAudio(context.Background(), exportFixtureConfig(), []Input{{Path: "source"}}, f.selection, 0, 2000, "video", "out", r, 1024, f)
	if err != f.capabilityErr || len(f.runs) != 0 {
		t.Fatal("capability error silently changed audio path", err, f.events)
	}
	f = newJoinedFixture()
	f.runErr = errors.New("staged process cause")
	f.copyErr = errors.New("classified copy cause")
	err = CopyAudioStaged(context.Background(), exportFixtureConfig(), []Input{{Path: "source"}}, f.selection, 0, 2000, "/workspace/video", "out", r, 1024, f)
	if err != f.copyErr || joinedEventCount(f.events, "copy") != 1 || joinedEventCount(f.events, "stat") != 0 || joinedEventCount(f.events, "remove:audio.mka") != 1 {
		t.Fatal("staged error classification or deferred cleanup changed", err, f.events)
	}
	f = newJoinedFixture()
	f.size = 1024
	err = CopyAudioStaged(context.Background(), exportFixtureConfig(), []Input{{Path: "source"}}, f.selection, 0, 2000, "/workspace/video", "out", r, 1024, f)
	if err != ErrOutputLimit || len(f.runs) != 1 || joinedEventCount(f.events, "remove:audio.mka") != 1 {
		t.Fatal("audio stage at its ceiling reached final mux", err, f.events)
	}
}

func TestJoinedVerificationPreservesBoundsAndCausePrecedence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*joinedFixture, *edit.ExportRequest)
		want   error
		text   string
		probes int
	}{
		{"stat error", func(f *joinedFixture, _ *edit.ExportRequest) { f.statErr = errors.New("stat cause") }, nil, "stat cause", 0},
		{"exact byte ceiling", func(f *joinedFixture, _ *edit.ExportRequest) { f.size = 1024 }, ErrOutputLimit, "", 0},
		{"probe cause", func(f *joinedFixture, _ *edit.ExportRequest) { f.probeErr = errors.New("probe cause") }, nil, "probe cause", 1},
		{"video before duration", func(f *joinedFixture, _ *edit.ExportRequest) { f.output.Streams = nil; f.duration = 9999 }, mediainfo.ErrUnsupported, "", 1},
		{"mp3 requires audio", func(f *joinedFixture, r *edit.ExportRequest) {
			r.Format = "mp3"
			f.output.Streams = f.output.Streams[:1]
		}, ErrNoAudio, "", 1},
		{"inclusive duration tolerance", func(f *joinedFixture, _ *edit.ExportRequest) { f.duration = 2350 }, nil, "", 1},
		{"duration beyond tolerance", func(f *joinedFixture, _ *edit.ExportRequest) { f.duration = 2351 }, nil, "joined result duration does not match its selected intervals", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r := newJoinedFixture(), exportFixtureRequest()
			f.size = 100
			tc.change(f, &r)
			info, err := VerifyJoined(context.Background(), "out", r, 2000, 1024, f)
			if (tc.want != nil && err != tc.want) || (tc.text != "" && (err == nil || err.Error() != tc.text)) || (tc.want == nil && tc.text == "" && err != nil) || joinedEventCount(f.events, "probe") != tc.probes {
				t.Fatal("verification result or error precedence changed", err, f.events)
			}
			if tc.probes > 0 && !reflect.DeepEqual(info, f.output) {
				t.Fatal("verification lost the original probe metadata on error", info)
			}
		})
	}
}

func TestJoinedPlanPreservesEscapingFingerprintsAndPacketEligibility(t *testing.T) {
	if got := FFConcatFile("a'b.mp4"); got != "file 'a'\\''b.mp4'\n" {
		t.Fatalf("concat escaping changed: %q", got)
	}
	a := &mediainfo.Stream{Index: 1, CodecName: "h264", TimeBase: "1/1000", Width: 160, Height: 90}
	b := *a
	b.ColorTransfer = "smpte2084"
	b.Tags.Language = "rus"
	b.Disposition.Default = 1
	if !SameJoinedStream(a, &b) || SameJoinedStream(nil, a) || !SameJoinedStream(nil, nil) {
		t.Fatal("joined fingerprint started comparing unrelated fields")
	}
	b.Width++
	if SameJoinedStream(a, &b) {
		t.Fatal("joined fingerprint ignored geometry")
	}
	b = *a
	angle := 360.0
	b.SideDataList = []mediainfo.SideData{{Rotation: &angle}}
	if SameJoinedStream(a, &b) {
		t.Fatal("joined fingerprint normalized formerly distinct display rotations")
	}
	for _, tc := range []struct {
		codec, clock, timebase, format string
		want                           bool
	}{
		{"aac", "0", "1/1000", "mkv", true}, {"mp3", "-1", "1/1000", "mkv", true}, {"flac", "0", "2/2000", "mkv", true}, {"aac", "0", "1/48000", "mkv", false}, {"aac", "NaN", "1/1000", "mkv", false}, {"opus", "0", "1/1000", "mkv", false}, {"aac", "0", "1/1000", "mp4", false},
	} {
		s := Selection{Audio: &mediainfo.Stream{CodecName: tc.codec, StartTime: tc.clock, TimeBase: tc.timebase}}
		if CopyAudioPacketTrimEligible(s, edit.ExportRequest{Format: tc.format}) != tc.want {
			t.Fatal("packet trim eligibility expanded beyond proved clock family", tc)
		}
	}
}
