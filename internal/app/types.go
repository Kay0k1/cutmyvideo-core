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
	MediaRemoved     bool       `json:"media_removed"`
	ID               string     `json:"id"`
	ExpiresAt        *time.Time `json:"expires_at"`
	RetentionBlocked bool       `json:"retention_blocked"`
	Title            string     `json:"title"`
	DurationMS       int64      `json:"duration_ms"`
	Kind             string     `json:"kind"`
	Provider         string     `json:"provider"`
	ProviderVideoID  *string    `json:"provider_video_id"`
	SourceURL        *string    `json:"source_url"`
	PreviewKind      string     `json:"preview_kind"`
	ThumbnailPath    string     `json:"-"`
	PreviewURL       *string    `json:"preview_url"`
	EmbedURL         *string    `json:"embed_url"`
	ThumbnailURL     *string    `json:"thumbnail_url"`
	Width            int        `json:"width,omitempty"`
	Height           int        `json:"height,omitempty"`
	Tracks           []Track    `json:"tracks,omitempty"`
	// Display dimensions are computed only by fresh local inspection. They are
	// not persisted in the source table or exposed by HTTP source responses.
	DisplayWidth  int    `json:"-"`
	DisplayHeight int    `json:"-"`
	Path          string `json:"-"`
	URL           string `json:"-"`
	Owner         string `json:"-"`
	StorageToken  string `json:"-"`
	ProviderID    string `json:"-"`
}

type Range struct {
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Label   string `json:"label"`
}

type ExportRequest struct {
	SourceID      string  `json:"source_id"`
	Ranges        []Range `json:"ranges"`
	Format        string  `json:"format"`
	Quality       string  `json:"quality"`
	CutMode       string  `json:"cut_mode"`
	OutputMode    string  `json:"output_mode,omitempty"`
	AudioTrack    *int    `json:"audio_track,omitempty"`
	NoAudio       bool    `json:"no_audio,omitempty"`
	SubtitleMode  string  `json:"subtitle_mode,omitempty"`
	Crop          *Crop   `json:"crop,omitempty"`
	CleanupSource bool    `json:"cleanup_source,omitempty"`
}

func (r *ExportRequest) Validate(c Config, s Source) error {
	if err := validateCropMode(*r); err != nil {
		return err
	}
	if len(r.Ranges) == 0 {
		return errors.New("choose at least one range; mark a start and an end before exporting")
	}
	if len(r.Ranges) > c.MaxRanges {
		return fmt.Errorf("choose between 1 and %d ranges (received %d); remove extra ranges or submit another job", c.MaxRanges, len(r.Ranges))
	}
	if r.Format != "mp4" && r.Format != "mp3" && r.Format != "mkv" {
		return errors.New("format must be mp4, mp3, or mkv")
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
	if r.Format == "mkv" && r.CutMode != "copy" {
		return errors.New("MKV export requires copy mode; choose MP4 for accurate cutting")
	}
	if r.OutputMode != "" && r.OutputMode != "separate" && r.OutputMode != "joined" {
		return errors.New("output_mode must be separate or joined")
	}
	if r.SubtitleMode != "" && r.SubtitleMode != "omit" && r.SubtitleMode != "preserve" {
		return errors.New("subtitle_mode must be omit or preserve")
	}
	if r.AudioTrack != nil && *r.AudioTrack < 0 {
		return errors.New("audio_track must be a nonnegative source stream index; inspect the source tracks first")
	}
	if r.NoAudio && r.AudioTrack != nil {
		return errors.New("choose audio_track or no_audio, not both")
	}
	if r.NoAudio && r.Format == "mp3" {
		return errors.New("MP3 export requires audio; enable audio or choose a video format")
	}
	if r.AudioTrack != nil && s.Path == "" {
		return errors.New("audio track selection requires an uploaded file; upload the source to select its audio track")
	}
	if r.SubtitleMode == "preserve" && (r.CutMode != "copy" || r.Format == "mp3") {
		return errors.New("subtitle preservation requires MP4 or MKV copy mode; choose copy mode or omit subtitles")
	}
	if r.SubtitleMode == "preserve" && s.Path == "" {
		return errors.New("subtitle preservation requires an uploaded file; upload the source or omit subtitles")
	}
	// Persist defaults exactly as older requests did. Idempotency continues to
	// return the original owner/key pair regardless of later request changes.
	if r.OutputMode == "separate" {
		r.OutputMode = ""
	}
	if r.SubtitleMode == "omit" {
		r.SubtitleMode = ""
	}
	var total int64
	for i := range r.Ranges {
		v := &r.Ranges[i]
		v.Label = strings.TrimSpace(v.Label)
		context := fmt.Sprintf("range %d", i+1)
		if v.Label != "" {
			context += fmt.Sprintf(" (%q)", v.Label)
		}
		if len(v.Label) > 200 {
			return fmt.Errorf("%s: shorten the label to at most 200 UTF-8 bytes", context)
		}
		if v.StartMS < 0 {
			return fmt.Errorf("%s: start_ms must be at least 0", context)
		}
		if v.EndMS <= v.StartMS {
			return fmt.Errorf("%s: set end_ms after start_ms (%d)", context, v.StartMS)
		}
		if v.EndMS > s.DurationMS {
			return fmt.Errorf("%s: set end_ms at or before the source duration (%d ms)", context, s.DurationMS)
		}
		if v.EndMS-v.StartMS > c.MaxRangeMS {
			return fmt.Errorf("%s: shorten the range to at most %d ms", context, c.MaxRangeMS)
		}
		duration := v.EndMS - v.StartMS
		if duration > c.MaxJobMS-total {
			return fmt.Errorf("%s: total selected duration exceeds %d ms; shorten or remove ranges", context, c.MaxJobMS)
		}
		total += duration
	}
	return nil
}

// ArtifactSegment records actual source boundaries and the resulting joined
// timeline in user-specified order. Copy boundaries may include keyframe preroll.
type ArtifactSegment struct {
	Index         int    `json:"index"`
	Label         string `json:"label"`
	SourceStartMS int64  `json:"source_start_ms"`
	SourceEndMS   int64  `json:"source_end_ms"`
	OutputStartMS int64  `json:"output_start_ms"`
	OutputEndMS   int64  `json:"output_end_ms"`
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
