package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
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

func ConfigFromEnv() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), DataDir: env("DATA_DIR", "./data"), ListenAddr: env("LISTEN_ADDR", ":8080"), FFmpeg: env("FFMPEG_PATH", "ffmpeg"), FFprobe: env("FFPROBE_PATH", "ffprobe"), YTDLP: env("YTDLP_PATH", "yt-dlp"), MaxSourceBytes: 32 << 30, MaxOutputBytes: 16 << 30, MaxRanges: 32, MaxRangeMS: 12 * 60 * 60 * 1000, MaxJobMS: 24 * 60 * 60 * 1000, JobTimeout: 12 * time.Hour, SourceTimeout: 2 * time.Minute, ArtifactTTL: 24 * time.Hour, SecureCookie: os.Getenv("COOKIE_SECURE") == "true"}
	c.PublicOrigin = os.Getenv("PUBLIC_ORIGIN")
	c.MaxFetchBytes = 32 << 30
	c.UploadTimeout = 2 * time.Hour
	c.WorkerHealthPath = workerHealthPath(os.Getenv("WORKER_HEALTH_PATH"))
	c.SourceTTL = 24 * time.Hour
	c.MaxStorageBytes = 96 << 30
	c.StorageSafetyBytes = 512 << 20
	c.StorageWaitTimeout = 30 * time.Minute
	c.MaxOwnerBytes = 64 << 30
	c.MaxActiveJobs = 32
	c.MutationsPerMinute = 20
	c.TrustProxy = os.Getenv("TRUST_PROXY") == "true"
	c.FFmpegProfile = env("FFMPEG_PROFILE", "fast")
	if c.FFmpegProfile != "fast" && c.FFmpegProfile != "compact" {
		return c, errors.New("invalid FFMPEG_PROFILE: use fast or compact")
	}
	var err error
	c.WorkerConcurrency, err = strconv.Atoi(env("WORKER_CONCURRENCY", "1"))
	if err != nil || c.WorkerConcurrency < 1 || c.WorkerConcurrency > 8 {
		return c, errors.New("invalid WORKER_CONCURRENCY: use 1 through 8")
	}
	c.WorkerPreparationConcurrency, err = strconv.Atoi(env("WORKER_PREPARATION_CONCURRENCY", "0"))
	if err != nil || c.WorkerPreparationConcurrency < 0 || c.WorkerPreparationConcurrency > 8 {
		return c, errors.New("invalid WORKER_PREPARATION_CONCURRENCY: use 0 through 8")
	}
	c.FFmpegThreads, err = strconv.Atoi(env("FFMPEG_THREADS", "2"))
	if err != nil || c.FFmpegThreads < 1 || c.FFmpegThreads > 32 {
		return c, errors.New("invalid FFMPEG_THREADS: use 1 through 32")
	}
	// Parse process settings once, rather than reading shared environment during
	// an export. FFmpeg's threads are separate from the Go runtime's CPU budget.
	for key, ptr := range map[string]*int64{"MAX_SOURCE_BYTES": &c.MaxSourceBytes, "MAX_FETCH_BYTES": &c.MaxFetchBytes, "MAX_OUTPUT_BYTES": &c.MaxOutputBytes, "MAX_STORAGE_BYTES": &c.MaxStorageBytes, "MAX_OWNER_BYTES": &c.MaxOwnerBytes, "MAX_RANGE_MS": &c.MaxRangeMS, "MAX_JOB_MS": &c.MaxJobMS} {
		if value := os.Getenv(key); value != "" {
			*ptr, err = strconv.ParseInt(value, 10, 64)
			if err != nil || *ptr <= 0 {
				return c, fmt.Errorf("invalid %s", key)
			}
		}
	}
	if value := os.Getenv("MAX_RANGES"); value != "" {
		c.MaxRanges, err = strconv.Atoi(value)
		if err != nil || c.MaxRanges <= 0 || c.MaxRanges > 128 {
			return c, errors.New("invalid MAX_RANGES: use 1 through 128")
		}
	}
	if value := os.Getenv("MAX_ACTIVE_JOBS"); value != "" {
		c.MaxActiveJobs, err = strconv.Atoi(value)
		if err != nil || c.MaxActiveJobs <= 0 || c.MaxActiveJobs > 1024 {
			return c, errors.New("invalid MAX_ACTIVE_JOBS: use 1 through 1024")
		}
	}
	if value := os.Getenv("MUTATIONS_PER_MINUTE"); value != "" {
		c.MutationsPerMinute, err = strconv.Atoi(value)
		if err != nil || c.MutationsPerMinute <= 0 {
			return c, errors.New("invalid MUTATIONS_PER_MINUTE")
		}
	}
	for key, ptr := range map[string]*time.Duration{"STORAGE_WAIT_TIMEOUT": &c.StorageWaitTimeout, "JOB_TIMEOUT": &c.JobTimeout, "SOURCE_TIMEOUT": &c.SourceTimeout, "UPLOAD_TIMEOUT": &c.UploadTimeout, "SOURCE_TTL": &c.SourceTTL, "ARTIFACT_TTL": &c.ArtifactTTL} {
		if value := os.Getenv(key); value != "" {
			*ptr, err = time.ParseDuration(value)
			if err != nil || *ptr <= 0 || *ptr > 30*24*time.Hour {
				return c, fmt.Errorf("invalid %s: use a positive duration up to 30 days", key)
			}
		}
	}
	if value := os.Getenv("STORAGE_SAFETY_BYTES"); value != "" {
		c.StorageSafetyBytes, err = strconv.ParseInt(value, 10, 64)
		if err != nil || c.StorageSafetyBytes < 0 {
			return c, errors.New("invalid STORAGE_SAFETY_BYTES")
		}
	}
	if c.MaxSourceBytes > (1<<63-1)-(2<<20) || c.MaxOutputBytes > (1<<63-1)/int64(c.MaxRanges) || c.MaxSourceBytes > ((1<<63-1)-c.MaxOutputBytes*int64(c.MaxRanges))/2 {
		return c, errors.New("storage limits overflow reservation budget")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	c.DataDir, err = filepath.Abs(c.DataDir)
	return c, err
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func uploadTimeout(c Config) time.Duration {
	if c.UploadTimeout > 0 {
		return c.UploadTimeout
	}
	return c.SourceTimeout
}

// The file upload cap and the transfer budget for a selected remote interval
// serve different purposes. Raising one must not inflate every job reservation.
func remoteSourceBudget(c Config) int64 {
	if c.MaxFetchBytes > 0 {
		return min(c.MaxSourceBytes, c.MaxFetchBytes)
	}
	return c.MaxSourceBytes
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
	Tracks           []Track            `json:"tracks,omitempty"`
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

type Range = edit.Range
type Track = edit.Track
type TrackSelection = edit.TrackSelection
type ArtifactSegment = edit.Segment

// ExportRequest retains its legacy validation method and JSON representation.
// Shared rules consume the neutral edit model through this compatibility type.
type ExportRequest edit.ExportRequest

func (r *ExportRequest) Validate(c Config, s Source) error {
	return edit.ValidateExportRequest((*edit.ExportRequest)(r), editLimits(c), editSource(s))
}

func editLimits(c Config) edit.Limits {
	return edit.Limits{MaxRanges: c.MaxRanges, MaxRangeMS: c.MaxRangeMS, MaxJobMS: c.MaxJobMS}
}

func editSource(s Source) edit.Source {
	return edit.Source{ID: s.ID, DurationMS: s.DurationMS, HasLocalMedia: s.Path != ""}
}

type Artifact struct {
	ID            string     `json:"id"`
	ExpiresAt     *time.Time `json:"expires_at"`
	Filename      string     `json:"filename"`
	SizeBytes     int64      `json:"size_bytes"`
	DownloadURL   string     `json:"download_url"`
	ActualStartMS int64      `json:"actual_start_ms"`
	ActualEndMS   int64      `json:"actual_end_ms"`
	// Pointers retain Artifact's comparable 1.x shape while adding optional
	// manifests. Both serialize as JSON arrays and persist in the job snapshot.
	Segments      *[]ArtifactSegment `json:"segments,omitempty"`
	TrackManifest *[]TrackSelection  `json:"track_manifest,omitempty"`
}

type JobItem struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	StartMS    int64     `json:"start_ms"`
	EndMS      int64     `json:"end_ms"`
	Status     string    `json:"status"`
	Artifact   *Artifact `json:"artifact"`
	Message    string    `json:"message,omitempty"`
	ErrorCode  string    `json:"error_code,omitempty"`
	ProgressMS int64     `json:"progress_ms,omitempty"`
}

type Job struct {
	StorageWaitUntil *time.Time    `json:"storage_wait_until"`
	ID               string        `json:"id"`
	Status           string        `json:"status"`
	Stage            string        `json:"stage"`
	Message          string        `json:"message"`
	Items            []JobItem     `json:"items"`
	Owner            string        `json:"-"`
	Request          ExportRequest `json:"-"`
	Cancelled        bool          `json:"-"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var ErrNotFound = errors.New("not found")
