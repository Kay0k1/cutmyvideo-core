package mediainfo

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func TestTrackDecisionPreservesManifestAndErrorContracts(t *testing.T) {
	tracks := []edit.Track{
		{Index: 0, Type: "video", Codec: "mjpeg", AttachedPicture: true},
		{Index: 4, Type: "video", Codec: "h264"},
		{Index: 7, Type: "audio", Codec: "aac", Language: " rus ", Title: " Alternate "},
		{Index: 2, Type: "audio", Codec: "aac", Default: true},
		{Index: 10, Type: "subtitle", Codec: "webvtt"},
		{Index: 11, Type: "data", Codec: "bin_data"},
	}
	before := append([]edit.Track(nil), tracks...)
	manifest, err := SelectExportTracks(tracks, edit.ExportRequest{Format: "mp4", SubtitleMode: "preserve"})
	if err != nil {
		t.Fatal(err)
	}
	wantReasons := []string{"video_not_selected", "selected_video", "audio_not_selected", "selected_audio", "subtitles_preserved", "unsupported_stream"}
	wantIncluded := []bool{false, true, false, true, true, false}
	if len(manifest) != len(tracks) {
		t.Fatalf("track visibility changed: %+v", manifest)
	}
	for i, item := range manifest {
		if item.Index != tracks[i].Index || item.Reason != wantReasons[i] || item.Included != wantIncluded[i] {
			t.Fatalf("manifest[%d]=%+v, want reason=%s included=%v", i, item, wantReasons[i], wantIncluded[i])
		}
	}
	if manifest[2].Language != "rus" || manifest[2].Title != "Alternate" || !reflect.DeepEqual(tracks, before) {
		t.Fatal("track presentation changed or mutated the caller's metadata")
	}
	selected := 7
	manifest, err = SelectExportTracks(tracks, edit.ExportRequest{Format: "mp3", AudioTrack: &selected})
	if err != nil || manifest[1].Included || !manifest[2].Included || manifest[3].Included || manifest[4].Reason != "subtitles_omitted" {
		t.Fatalf("absolute audio selection or MP3 omission changed: %+v %v", manifest, err)
	}
	selected = 99
	manifest, err = SelectExportTracks(tracks, edit.ExportRequest{Format: "mp4", AudioTrack: &selected, NoAudio: true})
	if err != nil || manifest[2].Included || manifest[3].Included || manifest[2].Reason != "audio_disabled" || manifest[3].Reason != "audio_disabled" {
		t.Fatalf("NoAudio precedence changed: %+v %v", manifest, err)
	}
	unsupported := append([]edit.Track(nil), tracks...)
	unsupported[4].Codec = "ass"
	manifest, err = SelectExportTracks(unsupported, edit.ExportRequest{Format: "mp4", SubtitleMode: "preserve", AudioTrack: &selected})
	var problem *Problem
	if manifest != nil || !errors.As(err, &problem) || problem.Code != "audio_track_unavailable" || problem.Message != "The selected audio stream index is not available; inspect the source and choose an audio index" {
		t.Fatalf("audio validation no longer precedes subtitle validation: manifest=%+v err=%v", manifest, err)
	}
	manifest, err = SelectExportTracks(unsupported, edit.ExportRequest{Format: "mp4", SubtitleMode: "preserve"})
	if manifest != nil || !errors.As(err, &problem) || problem.Code != "subtitle_unsupported" || problem.Message != "Subtitle stream 10 uses ass; preserve supports plain text subrip, mov_text, webvtt and text. Choose omit for this source" || err.Error() != problem.Message || errors.Unwrap(err) != nil {
		t.Fatalf("subtitle failure contract changed: manifest=%+v err=%v", manifest, err)
	}
	empty, err := SelectExportTracks(nil, edit.ExportRequest{})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty manifest shape changed: %#v %v", empty, err)
	}
	if !SubtitleTextSupported("subrip") || !SubtitleTextSupported("mov_text") || !SubtitleTextSupported("webvtt") || !SubtitleTextSupported("text") || SubtitleTextSupported("ass") {
		t.Fatal("supported plain-text subtitle family changed")
	}
	if AudioMetadataArgs(nil) != nil {
		t.Fatal("nil audio produced metadata arguments")
	}
	audio := &Stream{}
	audio.Tags.Language, audio.Tags.Title = " rus ", " Alternate "
	if got, want := AudioMetadataArgs(audio), []string{"-disposition:a:0", "default", "-metadata:s:a:0", "language= rus ", "-metadata:s:a:0", "title= Alternate "}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audio metadata argument order/raw tag values changed: %#v", got)
	}
	if ErrUnsupported.Error() != "unsupported media stream" {
		t.Fatal("neutral unsupported sentinel changed")
	}
}
