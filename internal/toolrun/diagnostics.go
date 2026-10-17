package toolrun

import (
	"bytes"
	"strings"
)

type limitedBuffer struct {
	// Do not embed bytes.Buffer: its promoted ReadFrom method lets io.Copy
	// bypass this type's bounded Write, including os/exec's output copier.
	buffer bytes.Buffer
	limit  int
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if b.buffer.Len() < b.limit {
		keep := b.limit - b.buffer.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = b.buffer.Write(p[:keep])
	}
	return n, nil
}

// Only fixed categories leave the subprocess boundary. Raw diagnostics may
// contain signed CDN URLs, proxy credentials, or user-controlled file names.
// A category is diagnostic evidence, never permission to retry an export.
func FailureCategory(stderr string) string {
	text := strings.ToLower(stderr)
	if strings.Contains(text, "no such filter") && (strings.Contains(text, "zscale") || strings.Contains(text, "tonemap")) {
		return "hdr_conversion_unavailable"
	}
	if strings.Contains(text, "stream map") && strings.Contains(text, ":a:") && strings.Contains(text, "matches no streams") {
		return "missing_audio"
	}
	if strings.Contains(text, "no space left on device") {
		return "storage_full"
	}
	if strings.Contains(text, "not currently supported in container") || strings.Contains(text, "codec not supported in container") {
		return "copy_incompatible"
	}
	// Older muxers can reject a codec as experimental instead of using the
	// usual unsupported-container wording. Do not enable that flag silently.
	if strings.Contains(text, "support is experimental") && (strings.Contains(text, "mp4") || strings.Contains(text, "matroska")) {
		return "copy_incompatible"
	}
	for _, phrase := range []string{"login required", "login_required", "sign in to", "log in to", "only available for registered", "subscriber-only", "subscribers only", "private video", "password protected"} {
		if strings.Contains(text, phrase) {
			return "platform_access"
		}
	}
	for _, status := range []string{"401", "403", "404", "410"} {
		if strings.Contains(text, "http error "+status) || strings.Contains(text, "server returned "+status) {
			return "upstream_denied"
		}
	}
	for _, status := range []string{"408", "429", "500", "502", "503", "504"} {
		if strings.Contains(text, "http error "+status) || strings.Contains(text, "server returned "+status) {
			return "upstream_unavailable"
		}
	}
	if strings.Contains(text, "connection timed out") || strings.Contains(text, "operation timed out") {
		return "network_timeout"
	}
	if strings.Contains(text, "connection reset by peer") {
		return "network_reset"
	}
	if strings.Contains(text, "invalid data found when processing input") || strings.Contains(text, "unknown decoder") || strings.Contains(text, "unknown encoder") || strings.Contains(text, "not on whitelist") {
		return "unsupported_media"
	}
	return "unknown"
}
