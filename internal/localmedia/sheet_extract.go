package localmedia

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaexport"
	"github.com/Kay0k1/cutmyvideo-core/internal/toolrun"
)

// A first pass scales only frames that begin within this window before a
// requested time; ordinary recordings always have one there. Sparse or
// unindexed sources fall back to slower passes that keep the same rule.
const sheetSeekWindowMS = 250

// Requested times share one decoder pass when decoding the frames between
// them costs less than another seek, which first decodes from the preceding
// keyframe: half a keyframe interval on average. A third of the measured
// interval keeps a margin for an imprecise estimate; the base stands for the
// cost of starting a decoder. Measured on four cores with a one-hour 1080p
// H.264 recording and 10 s between keyframes, for 60 frames: one second apart
// 9.6 s by seeking and 2.7 s in shared passes; two seconds apart 10.6 s and
// 4.5 s; five seconds apart 6.4 s by seeking and 8.3 s in shared passes.
const sheetRunGapBase = 400
const sheetRunGapMax = 5000
const sheetRunGapUnknown = 1000
const sheetKeyframeProbeMS = 12000

// A run's frame selection lists one window per target; bounding the run
// bounds that expression.
const sheetRunMaxTargets = 256

// A scaled frame without its timestamp line means the tool's diagnostics are
// not what this reader understands; fail instead of waiting for the deadline.
const sheetTimestampWait = 15 * time.Second

var errSheetStop = errors.New("sheet decoder delivered every requested frame")

// sheetSource is one inspected recording and the picture size it contributes.
type sheetSource struct {
	number        int // counts from one, as reported in the index
	path          string
	session       *LocalExportSession
	videoIndex    int
	duration      int64
	startUS       int64 // container start time: source time zero
	width, height int
	filter        string
	hdr           bool
	runGap        int64 // measured once: largest gap worth one shared pass
}

// sheetTarget is one requested source time and the frame found for it.
type sheetTarget struct {
	timeMS  int64
	tile    sheetTile
	frameUS int64
	found   bool
}

// sheetFrameStream pairs the decoder's raw frames with their timestamps and
// resolves ascending targets while streaming, keeping at most two frames that
// no target owns. A target receives the last frame presented at or before its
// time: the frame a player shows at that moment.
type sheetFrameStream struct {
	ctx       context.Context
	source    *sheetSource
	targets   []*sheetTarget
	windowed  bool // only frames within the seek window of a target arrive
	clamp     bool // a time before the first frame takes that first frame
	frameSize int

	mu      sync.Mutex
	pts     []int64
	head    int
	infoErr error
	notify  chan struct{}

	cur, prev []byte
	filled    int
	prevUS    int64
	have      bool
	prevOwned bool
	next      int
}

func newSheetFrameStream(ctx context.Context, source *sheetSource, targets []*sheetTarget, windowed, clamp bool) *sheetFrameStream {
	size := source.width * source.height * 3
	return &sheetFrameStream{ctx: ctx, source: source, targets: targets, windowed: windowed, clamp: clamp, frameSize: size, notify: make(chan struct{}, 1), cur: make([]byte, size)}
}

func (s *sheetFrameStream) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := copy(s.cur[s.filled:], p)
		s.filled += n
		p = p[n:]
		if s.filled == s.frameSize {
			s.filled = 0
			if err := s.frame(); err != nil {
				return total - len(p), err
			}
		}
	}
	return total, nil
}

// timestamp returns the next frame's presentation time. The tool logs it
// before it writes the frame, so waiting is the exception.
func (s *sheetFrameStream) timestamp() (int64, error) {
	var timer *time.Timer
	for {
		s.mu.Lock()
		if s.infoErr != nil {
			s.mu.Unlock()
			return 0, s.infoErr
		}
		if s.head < len(s.pts) {
			pts := s.pts[s.head]
			s.head++
			if s.head == len(s.pts) {
				s.pts, s.head = s.pts[:0], 0
			}
			s.mu.Unlock()
			return pts, nil
		}
		s.mu.Unlock()
		if timer == nil {
			timer = time.NewTimer(sheetTimestampWait)
			defer timer.Stop()
		}
		select {
		case <-s.notify:
		case <-s.ctx.Done():
			return 0, s.ctx.Err()
		case <-timer.C:
			return 0, analysisMalformed()
		}
	}
}

func (s *sheetFrameStream) assign(target *sheetTarget, rgb []byte, at int64) {
	target.tile = sheetTile{rgb: rgb, width: s.source.width, height: s.source.height}
	target.frameUS, target.found = at, true
}

// previous gives a target the newest delivered frame. A windowed pass drops
// the frames between windows, so an older frame proves nothing about what is
// on screen: such a target stays unresolved for an open pass.
func (s *sheetFrameStream) previous(target *sheetTarget) bool {
	if !s.have || s.windowed && s.prevUS < (target.timeMS-sheetSeekWindowMS)*1000 {
		return false
	}
	s.assign(target, s.prev, s.prevUS)
	s.prevOwned = true
	return true
}

func (s *sheetFrameStream) frame() error {
	pts, err := s.timestamp()
	if err != nil {
		return err
	}
	at := pts - s.source.startUS
	owned := false
	// This frame begins after the target, so the previous frame is still on
	// screen at the target's time.
	for s.next < len(s.targets) && s.targets[s.next].timeMS*1000 < at {
		if !s.previous(s.targets[s.next]) && !s.have && s.clamp {
			s.assign(s.targets[s.next], s.cur, at)
			owned = true
		}
		s.next++
	}
	spare := s.prev
	if spare == nil || s.prevOwned {
		spare = make([]byte, s.frameSize)
	}
	s.prev, s.prevUS, s.have, s.prevOwned, s.cur = s.cur, at, true, owned, spare
	if !s.windowed && s.next == len(s.targets) {
		return errSheetStop
	}
	return nil
}

// finish resolves the targets that no later frame followed: the stream ended
// or was trimmed at the last requested time, so the newest frame is theirs.
func (s *sheetFrameStream) finish() error {
	s.mu.Lock()
	pending, infoErr := len(s.pts)-s.head, s.infoErr
	s.mu.Unlock()
	if infoErr != nil {
		return infoErr
	}
	if s.filled != 0 || pending != 0 {
		return analysisMalformed()
	}
	for ; s.next < len(s.targets); s.next++ {
		s.previous(s.targets[s.next])
	}
	return nil
}

func (s *sheetFrameStream) info(line string) {
	pts, frame, err := parseSheetFrameInfo(line)
	if !frame {
		return
	}
	s.mu.Lock()
	if err != nil {
		s.infoErr = err
	} else {
		s.pts = append(s.pts, pts)
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// parseSheetFrameInfo reads one showinfo frame line. settb=AVTB precedes the
// filter, so pts is an exact count of microseconds rather than a rounded
// decimal. Other diagnostics, including showinfo's own extra lines, are not
// frame lines.
func parseSheetFrameInfo(line string) (pts int64, frame bool, err error) {
	at := strings.Index(line, "] n:")
	if at < 0 || !strings.Contains(line[:at], "showinfo") {
		return 0, false, nil
	}
	_, rest, ok := strings.Cut(line[at:], " pts:")
	if !ok {
		return 0, true, analysisMalformed()
	}
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return 0, true, analysisMalformed()
	}
	pts, err = strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, true, analysisMalformed()
	}
	return pts, true, nil
}

// sheetInfoWriter splits bounded diagnostic lines without retaining them.
type sheetInfoWriter struct {
	stream *sheetFrameStream
	line   [2048]byte
	length int
	skip   bool
}

func (w *sheetInfoWriter) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n')
		chunk := p
		if end >= 0 {
			chunk = p[:end]
		}
		if len(chunk) > len(w.line)-w.length {
			w.skip = true
		} else if !w.skip {
			w.length += copy(w.line[w.length:], chunk)
		}
		if end < 0 {
			break
		}
		if !w.skip {
			w.stream.info(string(w.line[:w.length]))
		}
		w.length, w.skip = 0, false
		p = p[end+1:]
	}
	return total, nil
}

// sheetWindows selects the frames that begin within the seek window of any
// ascending target, on the source clock in microseconds, and ends the stream
// after the last target. Overlapping windows merge into one term. The final
// term passes the first frame after the last window on to trim, which ends
// the stream there; without it the decoder would run to the end of the file.
func sheetWindows(targets []*sheetTarget, startUS int64) string {
	var terms []string
	from, until := int64(0), int64(0)
	for i, target := range targets {
		a, b := (target.timeMS-sheetSeekWindowMS)*1000+startUS, target.timeMS*1000+startUS
		if i != 0 && a <= until {
			until = b
			continue
		}
		if i != 0 {
			terms = append(terms, fmt.Sprintf("between(pts,%d,%d)", from, until))
		}
		from, until = a, b
	}
	terms = append(terms, fmt.Sprintf("between(pts,%d,%d)", from, until), fmt.Sprintf("gt(pts,%d)", until))
	return fmt.Sprintf("select='%s',trim=end_pts=%d", strings.Join(terms, "+"), until+1)
}

// decode runs one decoder from the keyframe at seekMS and resolves ascending
// targets. A windowed pass scales only the frames inside the targets' seek
// windows and ends by itself at the last target; an open pass scales every
// frame and is stopped as soon as a frame after its target arrives.
func (s *sheetSource) decode(ctx context.Context, c Config, threads int, seekMS int64, targets []*sheetTarget, windowed, clamp bool) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := newSheetFrameStream(runCtx, s, targets, windowed, clamp)
	// Timestamps stay on the source clock (-copyts) in microseconds, so each
	// comparison uses the frame's own presentation time, not a seek offset
	// rounded to a coarse stream time base.
	filter := "settb=AVTB"
	if windowed {
		filter += "," + sheetWindows(targets, s.startUS)
	}
	filter += "," + s.filter + ",showinfo=checksum=0"
	args := []string{"-hide_banner", "-loglevel", "repeat+info", "-nostdin", "-nostats", "-copyts", "-max_alloc", "268435456", "-filter_threads", "1", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaexport.InputBounds(threads)...)
	// Decoding starts at the keyframe before the time itself; the stream then
	// selects frames by timestamp, so the tool's own trimming is not needed.
	args = append(args, "-noaccurate_seek", "-ss", seconds(seekMS), "-i", s.path, "-map", "0:"+strconv.Itoa(s.videoIndex), "-an", "-sn", "-dn",
		"-vf", filter, "-threads", "1", "-fps_mode", "passthrough", "-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	output := &analysisOutput{writer: stream, cancel: cancel}
	r := toolrun.RunOutputWithDiagnostics(runCtx, c.FFmpeg, args, output, &sheetInfoWriter{stream: stream})
	stopped := errors.Is(output.err, errSheetStop)
	observed := r.ProcessErr
	if stopped {
		observed = nil
	}
	observeProcess(ctx, c.FFmpeg, r.Elapsed, observed)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if stopped {
		return nil
	}
	if output.err != nil {
		return output.err
	}
	if r.ProcessErr != nil {
		return mediaerror.RenderedFailure(&mediaerror.ProcessFailure{Cause: r.ProcessErr, Category: r.Category}, s.hdr)
	}
	return stream.finish()
}

// resolve finds the frames for one run of ascending targets. Targets the
// windowed pass could not resolve are retried one by one from the keyframe
// at their time, then from progressively earlier points for containers whose
// seek lands after the wanted frame, and finally from the start, where a time
// before the first frame takes that frame.
func (s *sheetSource) resolve(ctx context.Context, c Config, threads int, targets []*sheetTarget) error {
	if err := s.decode(ctx, c, threads, targets[0].timeMS, targets, true, false); err != nil {
		return err
	}
	for _, target := range targets {
		if target.found {
			continue
		}
		seeks := []int64{target.timeMS}
		for _, back := range []int64{2000, 10000, 60000} {
			if target.timeMS > back {
				seeks = append(seeks, target.timeMS-back)
			}
		}
		if target.timeMS != 0 {
			seeks = append(seeks, 0)
		}
		for i, seek := range seeks {
			if err := s.decode(ctx, c, threads, seek, []*sheetTarget{target}, false, i == len(seeks)-1); err != nil {
				return err
			}
			if target.found {
				break
			}
		}
		if !target.found {
			return &mediaerror.Problem{Code: "unsupported_stream", Message: "The video has no decodable frame for a requested sheet time"}
		}
	}
	return nil
}

// sheetRunGap turns keyframe timestamps into the largest gap between two
// requested times that one shared decoder pass still covers.
func sheetRunGap(keyframes []int64) int64 {
	sorted := slices.Sorted(slices.Values(keyframes))
	intervals := make([]int64, 0, len(sorted))
	for i := 1; i < len(sorted); i++ {
		if sorted[i] > sorted[i-1] {
			intervals = append(intervals, sorted[i]-sorted[i-1])
		}
	}
	if len(intervals) == 0 {
		// No second keyframe within the probed stretch: seeking is expensive.
		return sheetRunGapMax
	}
	slices.Sort(intervals)
	return min(sheetRunGapMax, sheetRunGapBase+intervals[len(intervals)/2]/3)
}

// mergeGap measures the keyframe interval once, and only when some pair of
// ascending targets is close enough for the answer to matter.
func (s *sheetSource) mergeGap(ctx context.Context, c Config, targets []*sheetTarget) int64 {
	if s.runGap != 0 {
		return s.runGap
	}
	undecided := false
	for i := 1; i < len(targets) && !undecided; i++ {
		gap := targets[i].timeMS - targets[i-1].timeMS
		undecided = gap > sheetRunGapBase && gap <= sheetRunGapMax
	}
	if !undecided {
		return sheetRunGapBase
	}
	// ffprobe reads intervals on the container's own clock.
	from := max(0, min(targets[0].timeMS, s.duration-sheetKeyframeProbeMS)+s.startUS/1000)
	s.runGap = sheetRunGapUnknown
	if frames, err := probeKeyframes(ctx, c, s.path, false, s.videoIndex, from, from+sheetKeyframeProbeMS); err == nil {
		keyframes := make([]int64, 0, len(frames))
		for _, frame := range frames {
			keyframes = append(keyframes, frame.PTS)
		}
		s.runGap = sheetRunGap(keyframes)
	}
	return s.runGap
}

// sheetRuns groups ascending targets into decoder passes. limit keeps a dense
// selection divisible between the workers.
func sheetRuns(targets []*sheetTarget, gap int64, limit int) [][]*sheetTarget {
	var runs [][]*sheetTarget
	start := 0
	for i := 1; i <= len(targets); i++ {
		if i == len(targets) || targets[i].timeMS-targets[i-1].timeMS > gap || i-start >= limit {
			runs = append(runs, targets[start:i])
			start = i
		}
	}
	return runs
}

// sheetParallel runs tasks on a bounded pool and returns the first failure,
// cancelling the remaining work.
func sheetParallel(ctx context.Context, workers, tasks int, run func(context.Context, int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var group sync.WaitGroup
	var once sync.Once
	var first error
	next := make(chan int)
	for range min(workers, tasks) {
		group.Go(func() {
			for i := range next {
				if err := run(ctx, i); err != nil {
					once.Do(func() {
						first = err
						cancel()
					})
				}
			}
		})
	}
feed:
	for i := range tasks {
		select {
		case next <- i:
		case <-ctx.Done():
			break feed
		}
	}
	close(next)
	group.Wait()
	return first
}
