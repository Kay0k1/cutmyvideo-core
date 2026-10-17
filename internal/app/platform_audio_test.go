package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// The incident's 140-0 English dub precedes 140-1 Russian original, with
// identical 129.471 kbps. A bitrate-only selector therefore chooses the dub.
func TestPlatformOriginalAudioSelection(t *testing.T) {
	for _, language := range []string{"ru", "en", "ja", "es"} {
		for _, dubBitrate := range []float64{129.471, 256} {
			t.Run(fmt.Sprintf("%s/dub-%g", language, dubBitrate), func(t *testing.T) {
				var info platformInfo
				err := json.Unmarshal([]byte(fmt.Sprintf(`{"extractor_key":"Youtube","formats":[
					{"format_id":"137","url":"https://media.example/video.mp4","protocol":"https","ext":"mp4","vcodec":"h264","acodec":"none","height":1080},
					{"format_id":"140-0","url":"https://media.example/dub.m4a","protocol":"https","ext":"m4a","vcodec":"none","acodec":"aac","language":"dub","language_preference":-1,"abr":%g},
					{"format_id":"140-1","url":"https://media.example/original.m4a","protocol":"https","ext":"m4a","vcodec":"none","acodec":"aac","language":"%s","language_preference":10,"abr":129.471},
					{"format_id":"139-1","url":"https://media.example/original-low.m4a","protocol":"https","ext":"m4a","vcodec":"none","acodec":"aac","language":"%s","language_preference":10,"abr":48.783}
				]}`, dubBitrate, language, language)), &info)
				if err != nil {
					t.Fatal(err)
				}
				for _, output := range []string{"mp4", "mp3"} {
					for _, quality := range []string{"best", "1080p", "720p", "480p"} {
						streams, err := pickStreams(info, quality, output)
						if err != nil || len(streams) == 0 {
							t.Fatalf("%s/%s: %v", output, quality, err)
						}
						if audio := streams[len(streams)-1]; audio.ID != "140-1" || audio.Language != language {
							t.Fatalf("%s/%s selected %s (%s)", output, quality, audio.ID, audio.Language)
						}
					}
				}
			})
		}
	}
}

func TestPlatformOriginalAudioRanksSupportedInputs(t *testing.T) {
	video := platformFormat{ID: "video", URL: "https://media.example/video.mp4", Protocol: "https", Ext: "mp4", VCodec: "h264", ACodec: "none", Height: 1080}
	original := platformFormat{ID: "original", URL: "https://media.example/audio.m4a", Protocol: "https", Ext: "m4a", VCodec: "none", ACodec: "aac", LanguagePreference: 10, ABR: 128}
	dub := original
	dub.ID, dub.LanguagePreference, dub.ABR = "dub", -1, 256
	combinedOriginal := video
	combinedOriginal.ID, combinedOriginal.ACodec, combinedOriginal.LanguagePreference, combinedOriginal.Height = "combined-original", "aac", 10, 720
	combinedDub := video
	combinedDub.ID, combinedDub.ACodec, combinedDub.LanguagePreference, combinedDub.Height = "combined-dub", "aac", -1, 2160
	hlsOriginal := original
	hlsOriginal.ID, hlsOriginal.Protocol, hlsOriginal.Ext = "hls-original", "m3u8_native", "mp4"
	drmOriginal := original
	drmOriginal.HasDRM = true
	unsafeOriginal := original
	unsafeOriginal.URL = "http://127.0.0.1/private.m4a"
	unknown, unknownBetter := original, dub
	unknown.LanguagePreference, unknownBetter.LanguagePreference = 0, 0
	for _, tt := range []struct {
		name, output string
		formats      []platformFormat
		want         string
	}{
		{"combined-original-before-resolution", "mp4", []platformFormat{combinedDub, combinedOriginal}, "combined-original"},
		{"separate-original-before-combined-dub", "mp4", []platformFormat{combinedDub, video, original}, "original"},
		{"combined-original-before-separate-dub", "mp4", []platformFormat{video, dub, combinedOriginal}, "combined-original"},
		{"mp3-combined-original-before-separate-dub", "mp3", []platformFormat{dub, combinedOriginal}, "combined-original"},
		{"mp3-separate-original-before-combined-dub", "mp3", []platformFormat{original, combinedDub}, "original"},
		{"mp3-hls-original", "mp3", []platformFormat{dub, hlsOriginal}, "hls-original"},
		{"unsupported-hls-pair", "mp4", []platformFormat{video, dub, hlsOriginal}, "dub"},
		{"drm-original", "mp4", []platformFormat{video, drmOriginal, dub}, "dub"},
		{"unsafe-original", "mp4", []platformFormat{video, unsafeOriginal, dub}, "dub"},
		{"unlabelled-audio-keeps-bitrate-ranking", "mp4", []platformFormat{video, unknown, unknownBetter}, "dub"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			streams, err := pickStreams(platformInfo{Formats: tt.formats}, "1080p", tt.output)
			if err != nil || len(streams) == 0 || streams[len(streams)-1].ID != tt.want {
				t.Fatalf("selected %v, %v; want %s", streams, err, tt.want)
			}
		})
	}
}

func TestPlatformOriginalAudioSurvivesExtractorAndCache(t *testing.T) {
	path := metadataExtractor(t)
	fixture := metadataFixture()
	formats := fixture["formats"].([]platformFormat)
	formats[1].Language, formats[1].LanguagePreference = "ja", 10
	dub := formats[1]
	dub.ID, dub.Language, dub.LanguagePreference, dub.ABR = "dub", "en", -1, 256
	fixture["formats"] = append(formats, dub)
	b, err := runMetadataFixture(t, path, metadataFixtureArgs(writeMetadataFixture(t, fixture), ""))
	if err != nil {
		t.Fatal(err)
	}
	var info platformInfo
	if err = json.Unmarshal(b, &info); err != nil {
		t.Fatal(err)
	}
	info.StreamSelectionVersion = platformStreamSelectionVersion
	payload, err := encodePlatformMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := metadataCacheFixture()
	cached, hit := cachedPlatformMetadata(source, payload, time.Now().Add(time.Minute))
	if !hit {
		t.Fatal("fresh original-audio metadata was not cacheable")
	}
	for _, output := range []string{"mp3", "mp4"} {
		streams, err := pickStreams(cached, "1080p", output)
		if err != nil || len(streams) == 0 || streams[len(streams)-1].ID != "251" || streams[len(streams)-1].Language != "ja" {
			t.Fatalf("%s lost original language through extraction/cache: %v, %v", output, streams, err)
		}
	}
	// Pre-fix rows remain decodable for rollout compatibility, but must miss
	// the lookup so a worker cannot reuse metadata that discarded preferences.
	info.StreamSelectionVersion = 0
	payload, err = encodePlatformMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	if _, hit = cachedPlatformMetadata(source, payload, time.Now().Add(time.Minute)); hit {
		t.Fatal("pre-fix metadata must be refreshed")
	}
}

func TestPlatformOriginalAudioInvalidatesPreviewCache(t *testing.T) {
	source := Source{ID: "source", URL: "https://www.youtube.com/watch?v=3V_wgHB4eFw"}
	sum := sha256.Sum256([]byte("media-contract-v3:source:0:default"))
	oldKey := hex.EncodeToString(sum[:])
	if got := previewCacheKeyOptions(source, 0, ExportRequest{}); got == oldKey {
		t.Fatal("platform preview reused pre-fix audio")
	}
	source.Path = "/data/upload.mp4"
	if got := previewCacheKeyOptions(source, 0, ExportRequest{}); got != oldKey {
		t.Fatal("uploaded media cache changed unnecessarily")
	}
}
