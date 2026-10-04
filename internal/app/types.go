package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, DataDir, ListenAddr, FFmpeg, FFprobe, YTDLP, PublicOrigin string
	WorkerHealthPath                                                       string
	MaxSourceBytes, MaxOutputBytes, MaxStorageBytes, MaxOwnerBytes         int64
	MaxRanges, MaxActiveJobs                                               int
	MutationsPerMinute                                                     int
	MaxRangeMS, MaxJobMS                                                   int64
	JobTimeout, SourceTimeout, SourceTTL, ArtifactTTL                      time.Duration
	SecureCookie, TrustProxy                                               bool
}

func ConfigFromEnv() (Config, error) {
	c := Config{DatabaseURL: os.Getenv("DATABASE_URL"), DataDir: env("DATA_DIR", "./data"), ListenAddr: env("LISTEN_ADDR", ":8080"), FFmpeg: env("FFMPEG_PATH", "ffmpeg"), FFprobe: env("FFPROBE_PATH", "ffprobe"), YTDLP: env("YTDLP_PATH", "yt-dlp"), MaxSourceBytes: 1 << 30, MaxOutputBytes: 1 << 30, MaxRanges: 12, MaxRangeMS: 600000, MaxJobMS: 3600000, JobTimeout: 30 * time.Minute, SourceTimeout: 2 * time.Minute, ArtifactTTL: 24 * time.Hour, SecureCookie: os.Getenv("COOKIE_SECURE") == "true"}
	c.PublicOrigin = os.Getenv("PUBLIC_ORIGIN")
	c.WorkerHealthPath = workerHealthPath(os.Getenv("WORKER_HEALTH_PATH"))
	c.SourceTTL = 24 * time.Hour
	c.MaxStorageBytes = 10 << 30
	c.MaxOwnerBytes = 2 << 30
	c.MaxActiveJobs = 32
	c.MutationsPerMinute = 20
	c.TrustProxy = os.Getenv("TRUST_PROXY") == "true"
	var err error
	for key, ptr := range map[string]*int64{"MAX_SOURCE_BYTES": &c.MaxSourceBytes, "MAX_OUTPUT_BYTES": &c.MaxOutputBytes, "MAX_STORAGE_BYTES": &c.MaxStorageBytes, "MAX_OWNER_BYTES": &c.MaxOwnerBytes, "MAX_RANGE_MS": &c.MaxRangeMS, "MAX_JOB_MS": &c.MaxJobMS} {
		if value := os.Getenv(key); value != "" {
			*ptr, err = strconv.ParseInt(value, 10, 64)
			if err != nil || *ptr <= 0 {
				return c, fmt.Errorf("invalid %s", key)
			}
		}
	}
	if value := os.Getenv("MAX_RANGES"); value != "" {
		c.MaxRanges, err = strconv.Atoi(value)
		if err != nil || c.MaxRanges <= 0 {
			return c, errors.New("invalid MAX_RANGES")
		}
	}
	if value := os.Getenv("MAX_ACTIVE_JOBS"); value != "" {
		c.MaxActiveJobs, err = strconv.Atoi(value)
		if err != nil || c.MaxActiveJobs <= 0 {
			return c, errors.New("invalid MAX_ACTIVE_JOBS")
		}
	}
	if value := os.Getenv("MUTATIONS_PER_MINUTE"); value != "" {
		c.MutationsPerMinute, err = strconv.Atoi(value)
		if err != nil || c.MutationsPerMinute <= 0 {
			return c, errors.New("invalid MUTATIONS_PER_MINUTE")
		}
	}
	for key, ptr := range map[string]*time.Duration{"JOB_TIMEOUT": &c.JobTimeout, "SOURCE_TIMEOUT": &c.SourceTimeout, "SOURCE_TTL": &c.SourceTTL, "ARTIFACT_TTL": &c.ArtifactTTL} {
		if value := os.Getenv(key); value != "" {
			*ptr, err = time.ParseDuration(value)
			if err != nil || *ptr <= 0 {
				return c, fmt.Errorf("invalid %s", key)
			}
		}
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

type Source struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	DurationMS      int64   `json:"duration_ms"`
	Kind            string  `json:"kind"`
	Provider        string  `json:"provider"`
	ProviderVideoID *string `json:"provider_video_id"`
	SourceURL       *string `json:"source_url"`
	PreviewKind     string  `json:"preview_kind"`
	ThumbnailPath   string  `json:"-"`
	PreviewURL      *string `json:"preview_url"`
	EmbedURL        *string `json:"embed_url"`
	ThumbnailURL    *string `json:"thumbnail_url"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
	Path            string  `json:"-"`
	URL             string  `json:"-"`
	Owner           string  `json:"-"`
	ProviderID      string  `json:"-"`
}

type Range struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Label   string `json:"label"`
}

type ExportRequest struct {
	SourceID string  `json:"source_id"`
	Ranges   []Range `json:"ranges"`
	Format   string  `json:"format"`
	Quality  string  `json:"quality"`
	CutMode  string  `json:"cut_mode"`
}

func (r *ExportRequest) Validate(c Config, s Source) error {
	if len(r.Ranges) == 0 || len(r.Ranges) > c.MaxRanges {
		return errors.New("choose between 1 and the allowed number of ranges")
	}
	if r.Format != "mp4" && r.Format != "mp3" {
		return errors.New("format must be mp4 or mp3")
	}
	if r.Quality != "1080p" && r.Quality != "720p" && r.Quality != "best" {
		return errors.New("quality must be 1080p, 720p, or best")
	}
	if r.CutMode != "accurate" && r.CutMode != "copy" {
		return errors.New("cut_mode must be accurate or copy")
	}
	if r.Format == "mp3" && r.CutMode == "copy" {
		return errors.New("MP3 export requires accurate mode; preserving arbitrary input audio as MP3 is not possible")
	}
	var total int64
	for i := range r.Ranges {
		v := &r.Ranges[i]
		v.Label = strings.TrimSpace(v.Label)
		if len(v.Label) > 200 {
			return errors.New("labels must be at most 200 bytes")
		}
		if v.StartMS < 0 || v.EndMS <= v.StartMS || v.EndMS > s.DurationMS {
			return errors.New("range must be within the source and end after its start")
		}
		if v.EndMS-v.StartMS > c.MaxRangeMS {
			return errors.New("range exceeds the duration limit")
		}
		total += v.EndMS - v.StartMS
		if total > c.MaxJobMS {
			return errors.New("total selected duration exceeds the limit")
		}
	}
	return nil
}

type Artifact struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	SizeBytes     int64  `json:"size_bytes"`
	DownloadURL   string `json:"download_url"`
	ActualStartMS int64  `json:"actual_start_ms"`
	ActualEndMS   int64  `json:"actual_end_ms"`
}

type JobItem struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	StartMS   int64     `json:"start_ms"`
	EndMS     int64     `json:"end_ms"`
	Status    string    `json:"status"`
	Artifact  *Artifact `json:"artifact"`
	Message   string    `json:"message,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
}

type Job struct {
	ID        string        `json:"id"`
	Status    string        `json:"status"`
	Stage     string        `json:"stage"`
	Message   string        `json:"message"`
	Items     []JobItem     `json:"items"`
	Owner     string        `json:"-"`
	Request   ExportRequest `json:"-"`
	Cancelled bool          `json:"-"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

var ErrNotFound = errors.New("not found")
