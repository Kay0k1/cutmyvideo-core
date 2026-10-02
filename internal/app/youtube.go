package app

import (
	"errors"
	"net/url"
	"strings"
)

var errInvalidYouTubeURL = errors.New("invalid YouTube video link")

func isYouTubeHost(host string) bool {
	switch strings.ToLower(strings.TrimSuffix(host, ".")) {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "music.youtube.com", "youtu.be", "www.youtu.be", "youtube-nocookie.com", "www.youtube-nocookie.com":
		return true
	}
	return false
}

func resemblesYouTubeHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, domain := range []string{"youtube.com", "youtube-nocookie.com", "youtu.be"} {
		if host == domain || strings.HasPrefix(host, domain+".") || strings.HasSuffix(host, "."+domain) || strings.Contains(host, "."+domain+".") {
			return true
		}
	}
	return false
}

// normalizeYouTubeURL accepts only known YouTube hosts. No original hostname,
// path, tracking parameters or timestamp is fetched after canonicalization.
func normalizeYouTubeURL(raw string) (*url.URL, error) {
	u, err := parsePastedURL(raw)
	if err != nil || !isYouTubeHost(u.Hostname()) {
		return nil, errInvalidYouTubeURL
	}
	if u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, errInvalidYouTubeURL
	}
	if p := u.Port(); p != "" && !((u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80")) {
		return nil, errInvalidYouTubeURL
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, errInvalidYouTubeURL
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	path := strings.Trim(u.Path, "/")
	parts := strings.Split(path, "/")
	var id string
	switch {
	case host == "youtu.be" || host == "www.youtu.be":
		if len(parts) != 1 {
			return nil, errInvalidYouTubeURL
		}
		id = parts[0]
	case host == "youtube-nocookie.com" || host == "www.youtube-nocookie.com":
		if len(parts) != 2 || parts[0] != "embed" {
			return nil, errInvalidYouTubeURL
		}
		id = parts[1]
	case path == "watch":
		values := query["v"]
		if len(values) != 1 {
			return nil, errInvalidYouTubeURL
		}
		id = values[0]
	case len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "live" || parts[0] == "embed"):
		id = parts[1]
	default:
		return nil, errInvalidYouTubeURL
	}
	if !validVideoID(id) {
		return nil, errInvalidYouTubeURL
	}
	return &url.URL{Scheme: "https", Host: "www.youtube.com", Path: "/watch", RawQuery: "v=" + id}, nil
}

func parsePastedURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	} else if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	return url.Parse(raw)
}

// Ordinary HTTP/schemeless pastes are accepted only for genuine YouTube links.
// The general core retains its existing explicit HTTPS policy for other media.
func normalizeSourceURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	u, err := parsePastedURL(trimmed)
	if err != nil {
		return nil, err
	}
	if isYouTubeHost(u.Hostname()) || resemblesYouTubeHost(u.Hostname()) {
		return normalizeYouTubeURL(trimmed)
	}
	return validateURL(trimmed)
}
