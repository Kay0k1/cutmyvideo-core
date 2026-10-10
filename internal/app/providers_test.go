package app

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestPopularPlatformNormalization(t *testing.T) {
	tests := []struct{ raw, canonical, provider string }{
		{"http://m.twitch.tv/videos/123?t=4m", "https://www.twitch.tv/videos/123", "twitch"},
		{"player.twitch.tv/?video=v123&parent=example.com", "https://www.twitch.tv/videos/123", "twitch"},
		{"twitch.tv/user/clip/Clip-ID_123?tt_medium=web", "https://clips.twitch.tv/Clip-ID_123", "twitch"},
		{"clips.twitch.tv/embed?clip=Clip-ID_123", "https://clips.twitch.tv/Clip-ID_123", "twitch"},
		{"http://www.rutube.ru/play/embed/3eac3b4561676c17df9132a9a1e62e3e/?t=40", "https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/", "rutube"},
		{"rutube.ru/live/video/3eac3b4561676c17df9132a9a1e62e3e/", "https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/", "rutube"},
		{"www.tiktok.com/@creator/video/123456789?utm_source=share", "https://www.tiktok.com/@creator/video/123456789", "tiktok"},
		{"vm.tiktok.com/ZExample/", "https://vm.tiktok.com/ZExample/", "tiktok"},
		{"instagram.com/reel/Clip_ID/?igsh=track", "https://www.instagram.com/reel/Clip_ID/", "instagram"},
		{"http://player.vimeo.com/video/123456?h=publichash#t=5", "https://player.vimeo.com/video/123456?h=publichash", "vimeo"},
		{"dai.ly/x12345?t=4", "https://www.dailymotion.com/video/x12345", "dailymotion"},
		{"vkvideo.ru/video-123_456?utm_campaign=test", "https://vkvideo.ru/video-123_456", "vk"},
		{"vk.com/video?z=video-123_456%2Fpl_123", "https://vk.com/video-123_456", "vk"},
		{"m.facebook.com/watch/?v=123456&fbclid=tracking", "https://m.facebook.com/watch/?v=123456", "facebook"},
		{"twitter.com/creator/status/123456?t=5", "https://x.com/creator/status/123456", "x"},
		{"old.reddit.com/r/videos/comments/abc123/title/", "https://old.reddit.com/r/videos/comments/abc123/title/", "reddit"},
		{"ok.ru/video/123456", "https://ok.ru/video/123456", "ok"},
		{"bilibili.com/video/BV1Example?p=2", "https://bilibili.com/video/BV1Example?p=2", "bilibili"},
		{"www.tiktok.com/t/ZTRC5xgJp/?utm_source=share", "https://www.tiktok.com/t/ZTRC5xgJp/", "tiktok"},
		{"instagram.com/share/reel/Example_ID/", "https://www.instagram.com/share/reel/Example_ID/", "instagram"},
		{"rutube.ru/shorts/3eac3b4561676c17df9132a9a1e62e3e/?p=publicHash&t=4", "https://rutube.ru/video/3eac3b4561676c17df9132a9a1e62e3e/?p=publicHash", "rutube"},
		{"rutube.ru/video/private/3eac3b4561676c17df9132a9a1e62e3e/?p=publicHash", "https://rutube.ru/video/private/3eac3b4561676c17df9132a9a1e62e3e/?p=publicHash", "rutube"},
		{"vk.com/video?z=video-123_456%2Fpl_123&access_key=publicHash", "https://vk.com/video-123_456?access_key=publicHash", "vk"},
		{"streamable.com/abc123", "https://streamable.com/abc123", "streamable"},
		{"rumble.com/v12345-example.html", "https://rumble.com/v12345-example.html", "rumble"},
	}
	for _, tt := range tests {
		t.Run(tt.provider+tt.raw, func(t *testing.T) {
			u, e := normalizeSourceURL(tt.raw)
			if e != nil || u.String() != tt.canonical {
				t.Fatalf("got %v,%v want %s", u, e, tt.canonical)
			}
			if p := providerForHost(u.Hostname()); p != tt.provider {
				t.Fatalf("wrong provider %q", p)
			}
		})
	}
}
func TestPlatformNormalizationRejectsCredentialsCollectionsAndLive(t *testing.T) {
	for _, tt := range []struct{ raw, code string }{
		{"https://user:secret@twitch.tv/videos/123", "invalid_source_url"},
		{"https://rutube.ru:8443/video/3eac3b4561676c17df9132a9a1e62e3e/", "invalid_source_url"},
		{"https://instagram.com/reel/clip/extra", "unsupported_collection"},
		{"https://vimeo.com/channels/example", "unsupported_collection"},
		{"https://www.twitch.tv/creator/videos", "unsupported_collection"},
		{"https://www.twitch.tv/creator", "live_not_supported"},
		{"https://player.twitch.tv/?channel=creator", "live_not_supported"},
		{"https://www.tiktok.com/@creator/live", "live_not_supported"},
		{"https://vk.com/video_ext.php?oid=invalid&id=123", "unsupported_collection"},
		{"https://streamable.com/abc%2Fextra", "unsupported_collection"},
		{"https://twitch.tv/videos/123?x=%ZZ", "invalid_source_url"},
	} {
		t.Run(tt.raw, func(t *testing.T) {
			_, e := normalizeSourceURL(tt.raw)
			p := problemFromError(e)
			if p == nil || p.code != tt.code {
				t.Fatalf("got %v want %s", e, tt.code)
			}
		})
	}
	for _, host := range []string{"twitch.tv.evil.example", "evil.twitch.tv", "notrutube.ru", "instagram.com.evil", "evil.instagram.com", "tiktok.com.evil"} {
		if isPlatformHost(host) {
			t.Fatalf("spoofed platform host %s", host)
		}
	}
	const signed = "https://cdn.example.org/file.mp4?b=two&a=signed%2Bvalue"
	u, e := normalizeSourceURL(signed)
	if e != nil || u.String() != signed {
		t.Fatalf("altered signed direct query: %v", e)
	}
}
func TestPlatformMetadataRejectsFiniteLiveCollectionsDRMAndMissingDuration(t *testing.T) {
	base := platformInfo{ID: "video", Title: "Recording", Duration: 100, LiveStatus: "was_live"}
	if e := validatePlatformInfo(base); e != nil {
		t.Fatal(e)
	}
	tests := []struct {
		modify func(*platformInfo)
		err    error
	}{
		{func(i *platformInfo) { i.IsLive = true }, errLiveSource},
		{func(i *platformInfo) { i.LiveStatus = "is_live" }, errLiveSource},
		{func(i *platformInfo) { i.LiveStatus = "is_upcoming" }, errLiveSource},
		{func(i *platformInfo) { i.Type = "playlist" }, errCollection},
		{func(i *platformInfo) { i.Entries = json.RawMessage(`[]`) }, errCollection},
		{func(i *platformInfo) { i.HasDRM = true }, errUnsupportedStream},
		{func(i *platformInfo) { i.Duration = math.Inf(1) }, errPlatformUnavailable},
		{func(i *platformInfo) { i.Duration = 0 }, errPlatformUnavailable},
	}
	for _, tt := range tests {
		i := base
		tt.modify(&i)
		if e := validatePlatformInfo(i); !errors.Is(e, tt.err) {
			t.Fatalf("got %v want %v", e, tt.err)
		}
	}
}
func TestSourcePresentationNeverExposesSignedMediaOrRemoteThumbnails(t *testing.T) {
	source := Source{ID: "src_example", Kind: "platform", URL: "https://www.twitch.tv/videos/123", ProviderID: "v123", ThumbnailURL: pointerString("https://cdn.example/image?secret=token")}
	completeSourcePresentation(&source)
	if source.Provider != "twitch" || source.SourceURL == nil || *source.SourceURL != source.URL || source.ProviderVideoID == nil || *source.ProviderVideoID != "v123" || source.PreviewKind != "window" || source.ThumbnailURL != nil {
		t.Fatalf("unsafe presentation %+v", source)
	}
	source.ThumbnailPath = "/private/source.thumbnail"
	completeSourcePresentation(&source)
	if *source.ThumbnailURL != "/api/v1/sources/src_example/thumbnail" {
		t.Fatal("thumbnail not owner protected")
	}
	source.Kind = "direct"
	source.URL = "https://cdn.example/video.mp4?secret=token"
	source.Path = "/private/video"
	completeSourcePresentation(&source)
	if source.SourceURL != nil || source.Provider != "direct" || source.PreviewKind != "native" {
		t.Fatal("direct CDN URL leaked")
	}
}
func pointerString(s string) *string { return &s }
