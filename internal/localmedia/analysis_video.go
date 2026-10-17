package localmedia

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const analysisVideoMaxFrames = 180000
const analysisVideoMaxBytes = 64 << 20
const analysisVideoMaxLine = 1024

// AnalyzeVideo measures scene discontinuities and dark intervals in one local
// decode. It does not identify subjects, speech, chapter names or highlights.
func AnalyzeVideo(ctx context.Context, c Config, path string, options VideoAnalysisOptions) (VideoAnalysisResult, error) {
	o, err := videoAnalysisOptions(options)
	if err != nil {
		return VideoAnalysisResult{}, err
	}
	ctx, session, info, duration, err := analysisSource(ctx, c, path)
	if err != nil {
		return VideoAnalysisResult{}, err
	}
	defer session.Close()
	start, end, err := analysisWindow(o.StartMS, o.EndMS, duration)
	if err != nil {
		return VideoAnalysisResult{}, err
	}
	video := info.selectedVideo()
	if video == nil {
		return VideoAnalysisResult{}, analysisArgument("scene and black analysis require a playable video stream")
	}
	parser := newVideoAnalysisParser(start, end, video.Index, o)
	args := analysisInputArgs(c, session.Path, start, end)
	// Evaluate scene on every frame. metadata's output is an independently
	// bounded text stream; the null muxer emits no media bytes to stdout.
	// Output -t can feed a lookahead frame through filters before the muxer
	// discards it. Trim before measurement so no out-of-window event is parsed.
	filter := fmt.Sprintf("trim=duration=%s,select='gte(scene,0)',blackdetect=d=0:pix_th=%s:pic_th=%s,metadata=mode=print:file='pipe\\:1'", seconds(end-start), strconv.FormatFloat(o.BlackPixelThreshold, 'f', 6, 64), strconv.FormatFloat(o.BlackPictureRatio, 'f', 6, 64))
	args = append(args, "-map", "0:"+strconv.Itoa(video.Index), "-an", "-sn", "-dn", "-vf", filter, "-threads", "1", "-fps_mode", "passthrough", "-f", "null", "-")
	if err = runAnalysis(ctx, c, args, parser); err != nil {
		return VideoAnalysisResult{}, err
	}
	if err = analysisUnchanged(session); err != nil {
		return VideoAnalysisResult{}, err
	}
	if err = parser.finish(); err != nil {
		return VideoAnalysisResult{}, err
	}
	return parser.result, nil
}

// videoAnalysisParser never retains a metadata transcript. It processes one
// fixed-size line and one frame, keeping only bounded detected events.
type videoAnalysisParser struct {
	result       VideoAnalysisResult
	options      VideoAnalysisOptions
	line         [analysisVideoMaxLine]byte
	lineLength   int
	bytes        int64
	frames       int
	haveFrame    bool
	atMS         int64
	score        float64
	haveScore    bool
	blackStart   int64
	blackFrom    int64
	blackUntil   int64
	hasBlackFrom bool
	hasBlackEnd  bool
}

func newVideoAnalysisParser(start, end int64, track int, options VideoAnalysisOptions) *videoAnalysisParser {
	return &videoAnalysisParser{result: VideoAnalysisResult{Version: 1, StartMS: start, EndMS: end, VideoTrack: track, Scenes: make([]SceneChange, 0), Black: make([]AnalysisInterval, 0)}, options: options, blackStart: -1}
}

func (p *videoAnalysisParser) Write(data []byte) (int, error) {
	length := len(data)
	if int64(length) > analysisVideoMaxBytes-p.bytes {
		return 0, analysisLimit("video analysis exceeds its 64 MiB metadata budget; choose a shorter interval")
	}
	p.bytes += int64(length)
	for len(data) > 0 {
		at := bytes.IndexByte(data, '\n')
		if at < 0 {
			at = len(data)
		}
		if at > len(p.line)-p.lineLength {
			return 0, analysisMalformed()
		}
		copy(p.line[p.lineLength:], data[:at])
		p.lineLength += at
		data = data[at:]
		if len(data) > 0 {
			if err := p.parseLine(strings.TrimSuffix(string(p.line[:p.lineLength]), "\r")); err != nil {
				return 0, err
			}
			p.lineLength = 0
			data = data[1:]
		}
	}
	return length, nil
}

func (p *videoAnalysisParser) timestamp(value string) (int64, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || !finite(seconds) || seconds < 0 || seconds > float64(p.result.EndMS-p.result.StartMS)/1000+0.001 {
		return 0, analysisMalformed()
	}
	return min(p.result.EndMS, p.result.StartMS+int64(math.Round(seconds*1000))), nil
}

func (p *videoAnalysisParser) parseLine(line string) error {
	if strings.HasPrefix(line, "frame:") {
		if err := p.frame(); err != nil {
			return err
		}
		if p.frames >= analysisVideoMaxFrames {
			return analysisLimit("video analysis exceeds 180000 decoded frames; choose a shorter interval")
		}
		fields := strings.Fields(line)
		if len(fields) != 3 || !strings.HasPrefix(fields[1], "pts:") || !strings.HasPrefix(fields[2], "pts_time:") {
			return analysisMalformed()
		}
		index, err := strconv.Atoi(strings.TrimPrefix(fields[0], "frame:"))
		if err != nil || index != p.frames {
			return analysisMalformed()
		}
		at, err := p.timestamp(strings.TrimPrefix(fields[2], "pts_time:"))
		if err != nil || p.frames > 0 && at < p.atMS {
			return analysisMalformed()
		}
		p.haveFrame, p.atMS, p.haveScore = true, at, false
		p.hasBlackFrom, p.hasBlackEnd = false, false
		p.frames++
		return nil
	}
	if !p.haveFrame {
		return analysisMalformed()
	}
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return analysisMalformed()
	}
	switch key {
	case "lavfi.scene_score":
		if p.haveScore {
			return analysisMalformed()
		}
		score, err := strconv.ParseFloat(value, 64)
		if err != nil || !finite(score) || score < 0 || score > 1 {
			return analysisMalformed()
		}
		p.score, p.haveScore = score, true
	case "lavfi.black_start", "lavfi.black_end":
		at, err := p.timestamp(value)
		if err != nil || at != p.atMS {
			return analysisMalformed()
		}
		if key == "lavfi.black_start" {
			if p.hasBlackFrom {
				return analysisMalformed()
			}
			p.blackFrom, p.hasBlackFrom = at, true
		} else {
			if p.hasBlackEnd {
				return analysisMalformed()
			}
			p.blackUntil, p.hasBlackEnd = at, true
		}
	default:
		// Ignore unrelated frame metadata, but its bytes/line size still count.
	}
	return nil
}

func (p *videoAnalysisParser) frame() error {
	if !p.haveFrame {
		return nil
	}
	if !p.haveScore {
		return analysisMalformed()
	}
	if p.atMS > p.result.StartMS && p.score >= p.options.SceneThreshold {
		if len(p.result.Scenes)+len(p.result.Black) >= AnalysisMaxEvents {
			return analysisLimit("video analysis exceeds 4096 detected events; choose a shorter interval or a higher scene threshold")
		}
		if count := len(p.result.Scenes); count > 0 && p.result.Scenes[count-1].AtMS == p.atMS {
			p.result.Scenes[count-1].Score = max(p.result.Scenes[count-1].Score, p.score)
		} else {
			p.result.Scenes = append(p.result.Scenes, SceneChange{AtMS: p.atMS, Score: p.score})
		}
	}
	if p.hasBlackFrom {
		if p.blackStart >= 0 {
			return analysisMalformed()
		}
		p.blackStart = p.blackFrom
	}
	if p.hasBlackEnd {
		if p.blackStart < 0 {
			return analysisMalformed()
		}
		if err := p.closeBlack(p.blackUntil); err != nil {
			return err
		}
	}
	p.result.AnalyzedEndMS = p.atMS
	p.haveFrame = false
	return nil
}

func (p *videoAnalysisParser) closeBlack(until int64) error {
	if until < p.blackStart {
		return analysisMalformed()
	}
	if until-p.blackStart >= p.options.MinBlackMS {
		if len(p.result.Scenes)+len(p.result.Black) >= AnalysisMaxEvents {
			return analysisLimit("video analysis exceeds 4096 detected events; choose a shorter interval")
		}
		p.result.Black = append(p.result.Black, AnalysisInterval{StartMS: p.blackStart, EndMS: until})
	}
	p.blackStart = -1
	return nil
}

func (p *videoAnalysisParser) finish() error {
	if p.lineLength != 0 {
		return analysisMalformed()
	}
	if err := p.frame(); err != nil {
		return err
	}
	if p.frames == 0 {
		return analysisMalformed()
	}
	if p.blackStart >= 0 {
		return p.closeBlack(p.result.AnalyzedEndMS)
	}
	return nil
}
