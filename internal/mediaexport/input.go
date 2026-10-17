package mediaexport

import (
	"strconv"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const InputFormats = "mov,matroska,webm,mp3,wav,flac,ogg,aac,avi,mpegts"

// These are per-tool input/decoder bounds, alongside container memory limits.
// Limit stream discovery too: the service only uses one video/audio pair.
func InputBounds(threads int) []string {
	if threads < 1 || threads > 32 {
		threads = 2
	}
	return []string{"-threads", strconv.Itoa(threads), "-max_pixels", strconv.Itoa(mediainfo.MaxPixels), "-max_streams", strconv.Itoa(mediainfo.MaxStreams)}
}

func Seconds(ms int64) string { return strconv.FormatFloat(float64(ms)/1000, 'f', 3, 64) }
