package app

import (
	"time"
)

const previewWindowMS int64 = 30000

const previewQuickWindowMS int64 = 6000

const previewOutputBytes int64 = 16 << 20

const previewInputBytes int64 = 64 << 20

const previewReservationBytes = 2*previewInputBytes + previewOutputBytes

const previewTimeout = 90 * time.Second

const previewReservationTTL = 5 * time.Minute

const previewCacheTTL = 30 * time.Minute

const previewCacheBytes int64 = 512 << 20

const previewOwnerCacheBytes int64 = 128 << 20

const previewCacheEntries = 64

const previewOwnerCacheEntries = 16

type windowKind int

const (
	windowVideo windowKind = iota
	windowStoryboard
	windowWaveform
)
