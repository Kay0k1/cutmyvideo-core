package mediainfo

import (
	"strconv"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func (p *Info) Tracks() []edit.Track {
	tracks := make([]edit.Track, 0, len(p.Streams))
	for _, s := range p.Streams {
		tracks = append(tracks, edit.Track{Index: s.Index, Type: s.CodecType, Codec: s.CodecName, Language: strings.TrimSpace(s.Tags.Language), Title: strings.TrimSpace(s.Tags.Title), Channels: s.Channels, SampleRate: s.SampleRate, Default: s.Disposition.Default != 0, AttachedPicture: s.Disposition.AttachedPic != 0})
	}
	return tracks
}

func SubtitleTextSupported(codec string) bool {
	return codec == "subrip" || codec == "mov_text" || codec == "webvtt" || codec == "text"
}

func TrackManifest(p Info, video, audio *Stream, request edit.ExportRequest) ([]edit.TrackSelection, error) {
	manifest := make([]edit.TrackSelection, 0, len(p.Streams))
	for _, track := range p.Tracks() {
		item := edit.TrackSelection{Track: track, Reason: "unsupported_stream"}
		if track.Type == "video" {
			item.Reason = "video_not_selected"
		}
		if track.Type == "audio" {
			item.Reason = "audio_not_selected"
		}
		if track.Type == "video" && video != nil && track.Index == video.Index && request.Format != "mp3" {
			item.Included = true
			item.Reason = "selected_video"
		}
		if track.Type == "audio" && audio != nil && track.Index == audio.Index {
			item.Included = true
			item.Reason = "selected_audio"
		}
		if track.Type == "audio" && request.NoAudio {
			item.Reason = "audio_disabled"
		}
		if track.Type == "audio" && request.AudioReplace != nil {
			item.Included, item.Reason = false, "audio_replaced"
		}
		if track.Type == "subtitle" {
			item.Reason = "subtitles_omitted"
			if request.SubtitleMode == "preserve" {
				if !SubtitleTextSupported(track.Codec) {
					return nil, &Problem{"subtitle_unsupported", "Subtitle stream " + strconv.Itoa(track.Index) + " uses " + track.Codec + "; preserve supports plain text subrip, mov_text, webvtt and text. Choose omit for this source"}
				}
				item.Included = true
				item.Reason = "subtitles_preserved"
			}
		}
		manifest = append(manifest, item)
	}
	return manifest, nil
}

func AudioMetadataArgs(audio *Stream) []string {
	if audio == nil {
		return nil
	}
	args := []string{"-disposition:a:0", "default"}
	if audio.Tags.Language != "" {
		args = append(args, "-metadata:s:a:0", "language="+audio.Tags.Language)
	}
	if audio.Tags.Title != "" {
		args = append(args, "-metadata:s:a:0", "title="+audio.Tags.Title)
	}
	return args
}

// SelectExportTracks computes the same deterministic selection from inspected
// tracks without running another tool. All tracks remain visible in the result.
func SelectExportTracks(tracks []edit.Track, request edit.ExportRequest) ([]edit.TrackSelection, error) {
	var p Info
	for _, track := range tracks {
		stream := Stream{Index: track.Index, CodecType: track.Type, CodecName: track.Codec, Channels: track.Channels, SampleRate: track.SampleRate}
		stream.Tags.Language, stream.Tags.Title = track.Language, track.Title
		if track.Default {
			stream.Disposition.Default = 1
		}
		if track.AttachedPicture {
			stream.Disposition.AttachedPic = 1
		}
		p.Streams = append(p.Streams, stream)
	}
	video, audio := p.SelectedVideo(), p.SelectedAudio()
	if request.NoAudio {
		audio = nil
	} else if request.AudioTrack != nil {
		audio = nil
		for i := range p.Streams {
			stream := &p.Streams[i]
			if stream.CodecType == "audio" && stream.Index == *request.AudioTrack {
				audio = stream
				break
			}
		}
		if audio == nil {
			return nil, &Problem{"audio_track_unavailable", "The selected audio stream index is not available; inspect the source and choose an audio index"}
		}
	}
	return TrackManifest(p, video, audio, request)
}
