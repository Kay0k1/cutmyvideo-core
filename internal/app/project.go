package app

import (
	"errors"
	"math"
	"time"
)

type AgentProjectJob struct {
	ClipID  string        `json:"clip_id"`
	Job     Job           `json:"job"`
	Request ExportRequest `json:"request"`
	Current bool          `json:"current"`
}
type AgentProjectClip struct {
	ClipID  string        `json:"clip_id"`
	Request ExportRequest `json:"request"`
}
type AgentProject struct {
	Version         int                `json:"version"`
	ID              string             `json:"id"`
	Revision        int64              `json:"revision"`
	SourceID        string             `json:"source_id"`
	Source          Source             `json:"source"`
	Plan            CollectionPlan     `json:"plan"`
	ClipIDs         []string           `json:"clip_ids,omitempty"`
	Rendition       string             `json:"rendition"`
	Jobs            []AgentProjectJob  `json:"jobs"`
	Clips           []AgentProjectClip `json:"clips"`
	ExpiresAt       time.Time          `json:"expires_at"`
	AccessExpiresAt *time.Time         `json:"access_expires_at,omitempty"`
	Owner           string             `json:"-"`
	CollectionKey   string             `json:"-"`
}
type projectCreateRequest struct {
	SourceID      string           `json:"source_id"`
	Plan          CollectionPlan   `json:"plan"`
	ClipIDs       []string         `json:"clip_ids,omitempty"`
	Rendition     string           `json:"rendition,omitempty"`
	CollectionKey string           `json:"collection_key"`
	JobIDs        []string         `json:"job_ids,omitempty"`
	ClipJobs      []projectClipJob `json:"clip_jobs,omitempty"`
}
type projectClipJob struct {
	ClipID string `json:"clip_id"`
	JobID  string `json:"job_id"`
}
type projectEditRequest struct {
	Revision  int64          `json:"revision"`
	Plan      CollectionPlan `json:"plan"`
	ClipIDs   []string       `json:"clip_ids,omitempty"`
	Rendition string         `json:"rendition,omitempty"`
}
type projectScopeKey struct{}
type projectScope struct {
	ID, Actor string
	Current   map[string]Job
}
type projectJobBinding struct {
	Scope projectScope
	Clip  string
}

var errProjectConflict = errors.New("project revision conflict")
var errProjectLimit = errors.New("project limit reached")
var errProjectSelection = errors.New("invalid project selection")

func projectPrepared(plan CollectionPlan, selected []string, rendition string, source Source, c Config, identityOnly bool) ([]PreparedCollectionClip, error) {
	if plan.Input != "" {
		return nil, collectionInvalid("", "plan.input", "HTTP projects cannot contain filesystem inputs")
	}
	if identityOnly {
		c.MaxRanges = 128 * 128
		c.MaxRangeMS = math.MaxInt64
		c.MaxJobMS = math.MaxInt64
		source.DurationMS = math.MaxInt64
		source.Path = "project-identity-only"
	}
	return NormalizeCollection(plan, CollectionExecution{ClipIDs: selected, Rendition: rendition}, c, source)
}

func projectRetryableJob(job Job, now time.Time) bool {
	if job.Status == "failed" || job.Status == "cancelled" {
		return true
	}
	if job.Status != "succeeded" || len(job.Items) == 0 {
		return false
	}
	for _, item := range job.Items {
		if item.Artifact != nil && (item.Artifact.ExpiresAt == nil || item.Artifact.ExpiresAt.After(now)) {
			return false
		}
	}
	return true
}
