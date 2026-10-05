package app

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// This catalogue identifies page URLs, not CDN URLs. Actual import support is
// checked against the extractor result and available finite media formats.
var providerHosts = map[string]string{
	"twitch.tv": "twitch", "www.twitch.tv": "twitch", "m.twitch.tv": "twitch", "go.twitch.tv": "twitch", "clips.twitch.tv": "twitch", "player.twitch.tv": "twitch",
	"rutube.ru": "rutube", "www.rutube.ru": "rutube",
	"tiktok.com": "tiktok", "www.tiktok.com": "tiktok", "m.tiktok.com": "tiktok", "vm.tiktok.com": "tiktok", "vt.tiktok.com": "tiktok",
	"instagram.com": "instagram", "www.instagram.com": "instagram", "m.instagram.com": "instagram",
	"vimeo.com": "vimeo", "www.vimeo.com": "vimeo", "player.vimeo.com": "vimeo",
	"dailymotion.com": "dailymotion", "www.dailymotion.com": "dailymotion", "dai.ly": "dailymotion",
	"vk.com": "vk", "www.vk.com": "vk", "m.vk.com": "vk", "vkvideo.ru": "vk", "www.vkvideo.ru": "vk", "m.vkvideo.ru": "vk",
	"facebook.com": "facebook", "www.facebook.com": "facebook", "m.facebook.com": "facebook", "mbasic.facebook.com": "facebook", "fb.watch": "facebook",
	"x.com": "x", "www.x.com": "x", "twitter.com": "x", "www.twitter.com": "x", "mobile.twitter.com": "x",
	"reddit.com": "reddit", "www.reddit.com": "reddit", "old.reddit.com": "reddit", "np.reddit.com": "reddit", "v.redd.it": "reddit", "redd.it": "reddit",
	"ok.ru": "ok", "www.ok.ru": "ok", "m.ok.ru": "ok",
	"bilibili.com": "bilibili", "www.bilibili.com": "bilibili", "m.bilibili.com": "bilibili", "b23.tv": "bilibili",
	"streamable.com": "streamable", "www.streamable.com": "streamable",
	"rumble.com": "rumble", "www.rumble.com": "rumble",
}

func providerForHost(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if isYouTubeHost(host) {
		return "youtube"
	}
	return providerHosts[host]
}
func isPlatformHost(host string) bool { return providerForHost(host) != "" }

// Fixed, public-safe diagnostics cross the import boundary. Neither errors nor
// responses contain signed stream addresses or extractor stderr.
type sourceProblem struct{ code, message string }

func (e *sourceProblem) Error() string { return e.message }

var errLiveSource = &sourceProblem{"live_not_supported", "Live broadcasts are not supported yet. Paste a recording or clip link instead"}
var errCollection = &sourceProblem{"unsupported_collection", "Choose a single recorded video or clip, rather than a channel or playlist"}
var errPlatformAccess = &sourceProblem{"platform_access_required", "This video requires login, membership, or permission. Use an accessible recording or upload your file"}
var errPlatformUnavailable = &sourceProblem{"platform_unavailable", "The platform did not provide an accessible recording. Check the link, regional availability, or upload your file"}
var errUnsupportedStream = &sourceProblem{"unsupported_stream", "This video uses unsupported encryption or media streams. Upload an accessible file instead"}
var errInvalidPlatformURL = &sourceProblem{"invalid_source_url", "Paste a valid single-video or clip link without credentials or a custom port"}

func matches(pattern, value string) bool { ok, _ := regexp.MatchString(pattern, value); return ok }

func normalizePlatformURL(u *url.URL, provider string) (*url.URL, error) {
	if u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Opaque != "" {
		return nil, errInvalidPlatformURL
	}
	if p := u.Port(); p != "" && !((u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80")) {
		return nil, errInvalidPlatformURL
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errInvalidPlatformURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	path := strings.TrimSuffix(u.Path, "/")
	if strings.ContainsAny(path, "\\\x00\r\n") {
		return nil, errInvalidPlatformURL
	}
	u.Scheme = "https"
	u.Host = host
	u.Fragment = ""
	// Drop tracking and player offsets, preserving access hashes/signatures.
	for key := range q {
		if strings.HasPrefix(strings.ToLower(key), "utm_") {
			q.Del(key)
		}
	}
	for _, key := range []string{"t", "start", "time", "si", "feature", "fbclid", "igsh", "igshid", "share_app_id", "share_item_id"} {
		q.Del(key)
	}
	valid := false
	switch provider {
	case "twitch":
		var id string
		switch {
		case host == "player.twitch.tv":
			id = strings.TrimPrefix(q.Get("video"), "v")
		case matches(`^/videos/[0-9]+$`, path):
			id = strings.TrimPrefix(path, "/videos/")
		case matches(`^/[A-Za-z0-9_]+/(v|video)/[0-9]+$`, path):
			id = path[strings.LastIndex(path, "/")+1:]
		}
		if matches(`^[0-9]+$`, id) {
			u.Host = "www.twitch.tv"
			u.Path = "/videos/" + id
			q = url.Values{}
			valid = true
		} else if host == "clips.twitch.tv" && path == "/embed" && matches(`^[A-Za-z0-9_-]+$`, q.Get("clip")) {
			u.Path = "/" + q.Get("clip")
			q = url.Values{}
			valid = true
		} else if host == "clips.twitch.tv" && path != "/embed" && matches(`^/[A-Za-z0-9_-]+$`, path) {
			valid = true
			q = url.Values{}
		} else if matches(`^/([A-Za-z0-9_]+/)?clip/[A-Za-z0-9_-]+$`, path) {
			u.Host = "clips.twitch.tv"
			u.Path = "/" + path[strings.LastIndex(path, "/")+1:]
			q = url.Values{}
			valid = true
		} else if matches(`^/[A-Za-z0-9_]+$`, path) || q.Get("channel") != "" {
			return nil, errLiveSource
		}
	case "rutube":
		if matches(`^/(video|video/private|embed|play/embed|live/video|shorts)/[a-zA-Z0-9]{32}$`, path) {
			id := path[strings.LastIndex(path, "/")+1:]
			u.Host = "rutube.ru"
			u.Path = "/video/" + id + "/"
			if strings.Contains(path, "/private/") {
				u.Path = "/video/private/" + id + "/"
			}
			access := q.Get("p")
			q = url.Values{}
			if access != "" {
				q.Set("p", access)
			}
			valid = true
		} else if matches(`^/(video|play)/embed/[0-9]+$`, path) {
			valid = true
		}
	case "tiktok":
		valid = matches(`^/@[^/]+/video/[0-9]+$`, path) || matches(`^/v/[0-9]+\.html$`, path) || ((host == "vm.tiktok.com" || host == "vt.tiktok.com") && matches(`^/[A-Za-z0-9]+$`, path)) || matches(`^/t/[A-Za-z0-9]+$`, path)
		if strings.Contains(path, "/live") {
			return nil, errLiveSource
		}
	case "instagram":
		valid = matches(`^/([^/]+/)?(reel|reels|p|tv)/[A-Za-z0-9_-]+$`, path) || matches(`^/share/(reel|p)/[A-Za-z0-9_-]+$`, path)
		u.Host = "www.instagram.com"
		q = url.Values{}
	case "vimeo":
		valid = matches(`^/([0-9]+(/[A-Za-z0-9]+)?|video/[0-9]+|channels/[^/]+/[0-9]+|groups/[^/]+/videos/[0-9]+)$`, path)
	case "dailymotion":
		if host == "dai.ly" && matches(`^/[A-Za-z0-9]+$`, path) {
			u.Host = "www.dailymotion.com"
			u.Path = "/video" + path
			valid = true
		} else {
			valid = matches(`^/(video|embed/video)/[A-Za-z0-9]+(_[^/]*)?$`, path)
		}
	case "vk":
		valid = matches(`^/(video|clip)-?[0-9]+_[0-9]+$`, path) || (path == "/video_ext.php" && matches(`^-?[0-9]+$`, q.Get("oid")) && matches(`^[0-9]+$`, q.Get("id")))
		if strings.HasPrefix(q.Get("z"), "video") {
			z := strings.Split(q.Get("z"), "/")[0]
			if matches(`^video-?[0-9]+_[0-9]+$`, z) {
				u.Path = "/" + z
				q.Del("z")
				valid = true
			}
		}
	case "facebook":
		valid = matches(`^/([^/]+/videos/([0-9]+|[^/]+/[0-9]+)|reel/[0-9]+|share/(v|r)/[A-Za-z0-9]+)$`, path) || ((path == "/watch" || path == "/video.php") && matches(`^[0-9]+$`, q.Get("v"))) || (host == "fb.watch" && matches(`^/[A-Za-z0-9_-]+$`, path))
	case "x":
		valid = matches(`^/[^/]+/status/[0-9]+(/video/[0-9]+)?$`, path) || matches(`^/i/status/[0-9]+$`, path)
		u.Host = "x.com"
	case "reddit":
		valid = matches(`^/r/[^/]+/comments/[A-Za-z0-9]+(/[^/]*)?$`, path) || ((host == "v.redd.it" || host == "redd.it") && matches(`^/[A-Za-z0-9]+$`, path))
	case "ok":
		valid = matches(`^/(video|videoembed)/[0-9]+$`, path)
	case "bilibili":
		valid = matches(`^/video/(BV[A-Za-z0-9]+|av[0-9]+)$`, path) || (host == "b23.tv" && matches(`^/[A-Za-z0-9]+$`, path))
	case "streamable":
		valid = matches(`^/(e/)?[A-Za-z0-9]+$`, path) || matches(`^/s/[A-Za-z0-9]+(/[A-Za-z0-9]+)?$`, path)
	case "rumble":
		valid = matches(`^/v[A-Za-z0-9-]+\.html$`, path) || matches(`^/embed/v[A-Za-z0-9]+$`, path)
	}
	if !valid {
		return nil, errCollection
	}
	u.RawPath = ""
	u.RawQuery = q.Encode()
	return validateURL(u.String())
}

func providerForExtractor(extractor string) string {
	key := strings.ToLower(extractor)
	for prefix, provider := range map[string]string{"youtube": "youtube", "twitch": "twitch", "rutube": "rutube", "tiktok": "tiktok", "instagram": "instagram", "vimeo": "vimeo", "dailymotion": "dailymotion", "vk": "vk", "facebook": "facebook", "twitter": "x", "reddit": "reddit", "odnoklassniki": "ok", "bilibili": "bilibili", "streamable": "streamable", "rumble": "rumble"} {
		if strings.HasPrefix(key, prefix) {
			return provider
		}
	}
	return "generic"
}

func completeSourcePresentation(v *Source) {
	v.Title = normalizeSourceTitle(v.Title)
	if v.Provider == "" {
		v.Provider = providerForHostFromURL(v.URL)
	}
	if v.Kind == "upload" || v.Kind == "direct" {
		v.Provider = v.Kind
		v.SourceURL = nil
	} else {
		if v.Provider == "" {
			v.Provider = "generic"
		}
		if v.URL != "" {
			u := v.URL
			v.SourceURL = &u
		}
	}
	v.ProviderVideoID = nil
	if v.ProviderID != "" {
		id := v.ProviderID
		v.ProviderVideoID = &id
	}
	v.PreviewKind = "none"
	if v.Path != "" {
		u := "/api/v1/sources/" + v.ID + "/media"
		v.PreviewURL = &u
		v.PreviewKind = "native"
	} else if v.EmbedURL != nil {
		v.PreviewKind = "youtube"
	}
	v.ThumbnailURL = nil
	if v.ThumbnailPath != "" {
		u := "/api/v1/sources/" + v.ID + "/thumbnail"
		v.ThumbnailURL = &u
	}
}

// Extractor titles and URL basenames are untrusted. Keep persisted metadata and
// owner-list responses bounded without slicing in the middle of a UTF-8 rune.
func normalizeSourceTitle(raw string) string {
	const maxTitleBytes = 512
	var out strings.Builder
	out.Grow(min(len(raw), maxTitleBytes))
	for _, r := range raw {
		if unicode.IsControl(r) {
			continue
		}
		if out.Len()+utf8.RuneLen(r) > maxTitleBytes {
			break
		}
		out.WriteRune(r)
	}
	title := strings.TrimSpace(out.String())
	if title == "" {
		return "Video"
	}
	return title
}
func providerForHostFromURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return ""
	}
	return providerForHost(u.Hostname())
}
func problemFromError(err error) *sourceProblem {
	var p *sourceProblem
	if errors.As(err, &p) {
		return p
	}
	return nil
}
