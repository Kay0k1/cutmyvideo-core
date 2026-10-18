package coremodel

import "time"

// ImageArtifact is a derived image, not a zero-duration media export.
type ImageArtifact struct {
	ID               string     `json:"id"`
	ParentArtifactID string     `json:"parent_artifact_id"`
	Filename         string     `json:"filename"`
	Format           string     `json:"format"`
	SizeBytes        int64      `json:"size_bytes"`
	DownloadURL      string     `json:"download_url"`
	Width            int        `json:"width"`
	Height           int        `json:"height"`
	TimestampMS      int64      `json:"timestamp_ms"`
	ExpiresAt        *time.Time `json:"expires_at"`
}
