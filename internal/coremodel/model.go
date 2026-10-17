// Package coremodel retains compatibility types shared by the local engine and service.
// It has no environment, filesystem, network or database behavior.
package coremodel

import (
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

type Config struct {
	DatabaseURL, DataDir, ListenAddr, FFmpeg, FFprobe, YTDLP, PublicOrigin string
	WorkerHealthPath                                                       string
	FFmpegProfile                                                          string
	FFmpegThreads                                                          int
	WorkerConcurrency                                                      int
	WorkerPreparationConcurrency                                           int
	MaxSourceBytes, MaxOutputBytes, MaxStorageBytes, MaxOwnerBytes         int64
	MaxFetchBytes                                                          int64
	StorageSafetyBytes                                                     int64
	MaxRanges, MaxActiveJobs                                               int
	MutationsPerMinute                                                     int
	MaxRangeMS, MaxJobMS                                                   int64
	JobTimeout, SourceTimeout, SourceTTL, ArtifactTTL                      time.Duration
	StorageWaitTimeout                                                     time.Duration
	UploadTimeout                                                          time.Duration
	SecureCookie, TrustProxy                                               bool
}

type SourceFingerprint struct {
	Kind               string `json:"kind"`
	SHA256             string `json:"sha256,omitempty"`
	SizeBytes          int64  `json:"size_bytes,omitempty"`
	Provider           string `json:"provider,omitempty"`
	ProviderID         string `json:"provider_id,omitempty"`
	CanonicalURLSHA256 string `json:"canonical_url_sha256,omitempty"`
}

type Source struct {
	Fingerprint      *SourceFingerprint `json:"fingerprint,omitempty"`
	MediaRemoved     bool               `json:"media_removed"`
	ID               string             `json:"id"`
	ExpiresAt        *time.Time         `json:"expires_at"`
	RetentionBlocked bool               `json:"retention_blocked"`
	Title            string             `json:"title"`
	DurationMS       int64              `json:"duration_ms"`
	Kind             string             `json:"kind"`
	Provider         string             `json:"provider"`
	ProviderVideoID  *string            `json:"provider_video_id"`
	SourceURL        *string            `json:"source_url"`
	PreviewKind      string             `json:"preview_kind"`
	ThumbnailPath    string             `json:"-"`
	PreviewURL       *string            `json:"preview_url"`
	EmbedURL         *string            `json:"embed_url"`
	ThumbnailURL     *string            `json:"thumbnail_url"`
	Width            int                `json:"width,omitempty"`
	Height           int                `json:"height,omitempty"`
	Tracks           []edit.Track       `json:"tracks,omitempty"`
	// Display dimensions are computed only by fresh local inspection. They are
	// not persisted in the source table or exposed by HTTP source responses.
	DisplayWidth      int    `json:"-"`
	DisplayHeight     int    `json:"-"`
	Path              string `json:"-"`
	URL               string `json:"-"`
	Owner             string `json:"-"`
	StorageToken      string `json:"-"`
	ProviderID        string `json:"-"`
	PreparationKey    string `json:"-"`
	PreparationClaim  string `json:"-"`
	PreparationPathID string `json:"-"`
}

// ExportRequest retains its legacy validation method and JSON representation.
// Service and local engine names alias this single compatibility type.
type ExportRequest edit.ExportRequest

func (r *ExportRequest) Validate(c Config, s Source) error {
	return edit.ValidateExportRequest((*edit.ExportRequest)(r), EditLimits(c), EditSource(s))
}

func EditLimits(c Config) edit.Limits {
	return edit.Limits{MaxRanges: c.MaxRanges, MaxRangeMS: c.MaxRangeMS, MaxJobMS: c.MaxJobMS}
}

func EditSource(s Source) edit.Source {
	return edit.Source{ID: s.ID, DurationMS: s.DurationMS, HasLocalMedia: s.Path != ""}
}
