package engine

import (
	"context"
	"errors"
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/localmedia"
)

type QCOptions = localmedia.QCOptions
type QCReport = localmedia.QCReport
type QCContainer = localmedia.QCContainer
type QCStream = localmedia.QCStream
type QCVideo = localmedia.QCVideo
type QCAudio = localmedia.QCAudio
type QCSync = localmedia.QCSync
type QCCheck = localmedia.QCCheck
type QCGap = localmedia.QCGap
type QCInterval = localmedia.QCInterval
type QCColor = localmedia.QCColor
type QCIntervalStats = localmedia.QCIntervalStats

// QC measures a finished local file and never writes media: container format
// and fast start, video frame count and timing from packet timestamps (cfr or
// vfr, gaps, duplicate timestamps), black and frozen pictures, audio loudness,
// true peak, clipped samples and edge silence, and audio/video alignment. A
// single request covers at most 30 minutes, 180000 video frames and 256
// million decoded audio samples; gap and duplicate lists name at most 256
// entries, black and frozen intervals at most 4096.
//
// Invalid expectations fail with CodeInvalidArgument before any tool runs.
// When any expectation fails, the complete report is returned together with
// an Error whose Code is CodeExpectationFailed. Inspection uses
// InspectTimeout; the whole operation uses ExportTimeout.
func (e *Engine) QC(ctx context.Context, path string, options QCOptions) (report QCReport, err error) {
	defer func() { err = normalizeError(err) }()
	if e.configErr != nil {
		return report, invalidArgument(e.configErr)
	}
	ctx, cancel := context.WithTimeout(ctx, e.exportTimeout)
	defer cancel()
	config := e.config
	config.SourceTimeout = e.inspectTimeout
	report, err = localmedia.QC(ctx, config, path, options)
	if err != nil {
		return QCReport{}, err
	}
	if !report.Passed {
		var failed []string
		for _, check := range report.Checks {
			if check.Status != "pass" {
				failed = append(failed, check.Name)
			}
		}
		return report, &Error{Code: CodeExpectationFailed, Err: errors.New("expectations failed: " + strings.Join(failed, ", "))}
	}
	return report, nil
}
