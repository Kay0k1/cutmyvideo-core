package app

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

func twitchClipFormat(id string, height int) platformFormat {
	return platformFormat{ID: id, URL: "https://media.example/" + id + ".mp4", Protocol: "https", Ext: "mp4", Height: height}
}

func TestTwitchClipsPreferOriginalSupportedFamily(t *testing.T) {
	// The pinned extractor sorts portrait adaptations first because it gives
	// them quality=-2. The private legacy metadata payload only kept their IDs.
	portrait480, portrait720, portrait1080 := twitchClipFormat("portrait-480", 480), twitchClipFormat("portrait-720", 720), twitchClipFormat("portrait-1080", 1080)
	original720, original1080 := twitchClipFormat("720", 720), twitchClipFormat("1080", 1080)
	portrait1080.TBR = 10000
	for _, tt := range []struct {
		name    string
		formats []platformFormat
		want    string
	}{
		{"same-heights", []platformFormat{portrait480, portrait720, portrait1080, original720, original1080}, "1080"},
		{"taller-portrait", []platformFormat{twitchClipFormat("portrait-1920", 1920), original720}, "720"},
		{"original-above-cap", []platformFormat{portrait480, original1080}, "1080"},
		{"portrait-only", []platformFormat{portrait480, portrait720, portrait1080}, "portrait-1080"},
	} {
		for _, format := range []string{"mp4", "mp3"} {
			t.Run(tt.name+"/"+format, func(t *testing.T) {
				info := platformInfo{Extractor: "TwitchClips", Formats: tt.formats}
				for _, quality := range []string{"best", "1080p", "720p", "480p"} {
					want := tt.want
					if tt.name == "same-heights" && (quality == "720p" || quality == "480p") {
						want = "720"
					}
					if tt.name == "portrait-only" {
						if quality == "720p" {
							want = "portrait-720"
						}
						if quality == "480p" {
							want = "portrait-480"
						}
					}
					got, err := pickStreams(info, quality, format)
					if err != nil || len(got) != 1 || got[0].ID != want {
						t.Fatalf("%s selected %v, want %s: %v", quality, streamIDs(got), want, err)
					}
				}
			})
		}
	}
}

func TestTwitchClipsFamilyPreferenceRequiresSupportedOriginal(t *testing.T) {
	portrait := twitchClipFormat("portrait-720", 720)
	for _, tt := range []struct {
		name   string
		change func(*platformFormat)
	}{
		{"drm", func(f *platformFormat) { f.HasDRM = true }},
		{"protocol", func(f *platformFormat) { f.Protocol = "http_dash_segments" }},
		{"extension", func(f *platformFormat) { f.Ext = "mhtml" }},
		{"insecure-url", func(f *platformFormat) { f.URL = "http://media.example/insecure.mp4" }},
		{"video-only-no-audio", func(f *platformFormat) { f.VCodec, f.ACodec = "h264", "none" }},
		{"hls-video-only", func(f *platformFormat) { f.Protocol, f.VCodec, f.ACodec = "m3u8_native", "h264", "none" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			original := twitchClipFormat("1080", 1080)
			tt.change(&original)
			got, err := pickStreams(platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait, original}}, "best", "mp4")
			if err != nil || len(got) != 1 || got[0].ID != portrait.ID {
				t.Fatalf("selected %v: %v", streamIDs(got), err)
			}
		})
	}

	// The same compatibility rules allow the original progressive video to be
	// selected once there is progressive audio it can actually be paired with.
	original := twitchClipFormat("1080", 1080)
	original.VCodec, original.ACodec = "h264", "none"
	audio := platformFormat{ID: "audio", URL: "https://media.example/audio.m4a", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "aac", ABR: 128}
	got, err := pickStreams(platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait, original, audio}}, "best", "mp4")
	if err != nil || fmt.Sprint(streamIDs(got)) != "[1080 audio]" {
		t.Fatalf("selected %v: %v", streamIDs(got), err)
	}
}

func TestTwitchClipsActualMixedAspectQualities(t *testing.T) {
	// Sanitized format families from the reported 34-second clip: its cropped
	// adaptation is 900 pixels high while the original recording is 896.
	info := platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{
		twitchClipFormat("portrait-360", 360), twitchClipFormat("portrait-480", 480),
		twitchClipFormat("portrait-720", 720), twitchClipFormat("portrait-900", 900),
		twitchClipFormat("360", 360), twitchClipFormat("480", 480),
		twitchClipFormat("720", 720), twitchClipFormat("896", 896),
	}}
	for _, format := range []string{"mp4", "mp3"} {
		for quality, want := range map[string]string{"best": "896", "1080p": "896", "720p": "720", "480p": "480"} {
			got, err := pickStreams(info, quality, format)
			if err != nil || len(got) != 1 || got[0].ID != want {
				t.Fatalf("%s %s selected %v, want %s: %v", format, quality, streamIDs(got), want, err)
			}
		}
	}
}

func TestTwitchClipsPreferenceDoesNotReplaceOtherProvidersPortraitVideo(t *testing.T) {
	portrait := twitchClipFormat("portrait-1080", 1080)
	landscape := twitchClipFormat("720", 720)
	for _, extractor := range []string{"TikTok", "Youtube", "TwitchVod", "Generic", ""} {
		got, err := pickStreams(platformInfo{Extractor: extractor, Formats: []platformFormat{portrait, landscape}}, "best", "mp4")
		if err != nil || len(got) != 1 || got[0].ID != portrait.ID {
			t.Fatalf("%s selected %v: %v", extractor, streamIDs(got), err)
		}
	}
}

func TestTwitchClipsLegacyCachedMetadataKeepsOriginalSelection(t *testing.T) {
	// This is the complete legacy payload shape: no extractor preference,
	// aspect-ratio or newly added metadata fields are needed for the repair.
	legacy := []byte(`{"id":"fixture","title":"Clip","duration":30,"extractor_key":"TwitchClips","formats":[{"format_id":"portrait-1080","url":"https://media.example/portrait.mp4","protocol":"https","ext":"mp4","height":1080},{"format_id":"1080","url":"https://media.example/original.mp4","protocol":"https","ext":"mp4","height":1080}]}`)
	source := Source{ID: "src_fixture", Owner: "owner", URL: "https://clips.twitch.tv/Fixture", ProviderID: "fixture", DurationMS: 30000}
	info, ok := cachedPlatformMetadata(source, legacy, time.Now().Add(time.Minute))
	if !ok {
		t.Fatal("legacy Twitch clip metadata is not cacheable")
	}
	for _, payloadKind := range []string{"legacy", "round-trip"} {
		if payloadKind == "round-trip" {
			payload, err := encodePlatformMetadata(info)
			if err != nil {
				t.Fatal(err)
			}
			info, ok = cachedPlatformMetadata(source, payload, time.Now().Add(time.Minute))
			if !ok {
				t.Fatal("Twitch clip metadata stopped being cacheable")
			}
		}
		for _, quality := range []string{"best", "1080p", "720p", "480p"} {
			got, err := pickStreams(info, quality, "mp4")
			if err != nil || len(got) != 1 || got[0].ID != "1080" {
				t.Fatalf("%s %s selected %v: %v", payloadKind, quality, streamIDs(got), err)
			}
		}
	}
}

func TestPlatformSourceDimensionsDescribeSelectedEligibleMedia(t *testing.T) {
	original := twitchClipFormat("1080", 1080)
	original.Width = 1920
	portrait := twitchClipFormat("portrait-1920", 1920)
	portrait.Width = 1080
	verticalOriginal := original
	verticalOriginal.Width = 606
	for _, tt := range []struct {
		name          string
		info          platformInfo
		width, height int
	}{
		{"original-family", platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait, original}}, 1920, 1080},
		{"original-is-vertical", platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait, verticalOriginal}}, 606, 1080},
		{"portrait-only", platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait}}, 1080, 1920},
		{"native-portrait-provider", platformInfo{Extractor: "TikTok", Formats: []platformFormat{portrait, original}}, 1080, 1920},
		{"unknown-width", platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{portrait, twitchClipFormat("1080", 1080)}}, 0, 1080},
		{"audio-only", platformInfo{Formats: []platformFormat{{ID: "audio", URL: "https://media.example/audio.m4a", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "aac"}}}, 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			width, height := platformSourceDimensions(tt.info)
			if width != tt.width || height != tt.height {
				t.Fatalf("got %dx%d, want %dx%d", width, height, tt.width, tt.height)
			}
		})
	}
	unsupported := portrait
	unsupported.ID, unsupported.HasDRM = "2160", true
	width, height := platformSourceDimensions(platformInfo{Extractor: "TwitchClips", Formats: []platformFormat{unsupported, original}})
	if width != 1920 || height != 1080 {
		t.Fatalf("unsupported rendition changed source dimensions: %dx%d", width, height)
	}
}

func TestPlatformMetadataExtractorTwitchOriginalPreference(t *testing.T) {
	path := metadataExtractor(t)
	fixture := map[string]any{"id": "fixture", "title": "Clip", "duration": 30, "extractor": "twitch:clips", "extractor_key": "TwitchClips"}
	var formats []map[string]any
	for _, family := range []string{"portrait", "original"} {
		for _, height := range []int{480, 720, 1080} {
			id, ratio, quality := fmt.Sprint(height), 16.0/9.0, 0
			if family == "portrait" {
				id, ratio, quality = "portrait-"+id, 9.0/16.0, -2
			}
			formats = append(formats, map[string]any{"format_id": id, "url": "https://media.example/" + id + ".mp4", "protocol": "https", "ext": "mp4", "height": height, "aspect_ratio": ratio, "quality": quality})
		}
	}
	fixture["formats"] = formats
	b, err := runMetadataFixture(t, path, metadataFixtureArgs(writeMetadataFixture(t, fixture), ""))
	if err != nil {
		t.Fatal(err)
	}
	var info platformInfo
	if err := json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	if len(info.Formats) != len(formats) || info.Formats[0].ID != "portrait-480" {
		t.Fatal("real extractor did not reproduce portrait-first ordering")
	}
	for quality, want := range map[string]string{"best": "1080", "1080p": "1080", "720p": "720", "480p": "480"} {
		got, err := pickStreams(info, quality, "mp4")
		if err != nil || len(got) != 1 || got[0].ID != want {
			t.Fatalf("%s selected %v, want %s: %v", quality, streamIDs(got), want, err)
		}
	}
}

func streamIDs(formats []platformFormat) []string {
	ids := make([]string, len(formats))
	for i, f := range formats {
		ids[i] = f.ID
	}
	return ids
}
