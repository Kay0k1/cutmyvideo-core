package localmedia

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediaerror"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
	"github.com/Kay0k1/cutmyvideo-core/internal/mediaqc"
)

const qcPacketMaxLine = 256

// QCOptions selects the measured window and the expectations to check. Zero
// EndMS uses the source duration within the 30 minute analysis limit. Expect
// holds items such as "fps=30", "size=1080x1920", "duration=14.7±0.1" or "cfr".
// Zero MinBlackMS reports every black frame but the final one; zero MinFreezeMS
// uses 500 ms.
type QCOptions struct {
	StartMS     int64    `json:"start_ms"`
	EndMS       int64    `json:"end_ms"`
	Expect      []string `json:"expect,omitempty"`
	MinBlackMS  int64    `json:"min_black_ms"`
	MinFreezeMS int64    `json:"min_freeze_ms"`
}

type QCReport = mediaqc.Report
type QCContainer = mediaqc.Container
type QCStream = mediaqc.Stream
type QCVideo = mediaqc.Video
type QCAudio = mediaqc.Audio
type QCSync = mediaqc.Sync
type QCCheck = mediaqc.Check
type QCGap = mediaqc.Gap
type QCInterval = mediaqc.Interval
type QCColor = mediaqc.Color
type QCIntervalStats = mediaqc.IntervalStats

// QC measures a finished local file without writing media: container layout,
// video frame timing, black and frozen pictures, audio loudness, clipping and
// edge silence, and audio/video alignment. Expectations are validated before
// any tool runs; their outcome is in the report, not in the error.
func QC(ctx context.Context, c Config, path string, options QCOptions) (QCReport, error) {
	expectations, err := mediaqc.ParseExpectations(options.Expect)
	if err != nil {
		return QCReport{}, analysisArgument(err.Error())
	}
	if options.MinBlackMS == 0 {
		options.MinBlackMS = 1
	}
	if options.MinFreezeMS == 0 {
		options.MinFreezeMS = 500
	}
	if options.MinBlackMS < 1 || options.MinBlackMS > 60000 || options.MinFreezeMS < 100 || options.MinFreezeMS > 60000 {
		return QCReport{}, analysisArgument("use min_black_ms 1..60000 and min_freeze_ms 100..60000")
	}
	ctx, session, info, duration, err := analysisSource(ctx, c, path)
	if err != nil {
		return QCReport{}, err
	}
	defer session.Close()
	start, end, err := analysisWindow(options.StartMS, options.EndMS, duration)
	if err != nil {
		return QCReport{}, err
	}
	details, err := qcDetails(ctx, c, session.Path)
	if err != nil {
		return QCReport{}, err
	}
	origin := 0.0
	if value, ok := qcSeconds(info.Format.StartTime); ok {
		origin = value * 1000
	}
	report := QCReport{Version: 1, Path: session.Path, StartMS: start, EndMS: end, Checks: make([]QCCheck, 0)}
	report.Container = QCContainer{Format: details.Format.FormatName, DurationMS: duration, StartMS: int64(math.Round(origin)), Streams: make([]QCStream, 0, len(info.Streams))}
	report.Container.SizeBytes, _ = strconv.ParseInt(details.Format.Size, 10, 64)
	report.Container.BitRate, _ = strconv.ParseInt(details.Format.BitRate, 10, 64)
	for _, s := range info.Streams {
		stream := QCStream{Index: s.Index, Type: s.CodecType, Codec: s.CodecName}
		if value, ok := qcSeconds(s.StartTime); ok {
			at := int64(math.Round(value*1000 - origin))
			stream.StartMS = &at
		}
		if value, ok := qcSeconds(s.Duration); ok {
			length := int64(math.Round(value * 1000))
			stream.DurationMS = &length
		}
		report.Container.Streams = append(report.Container.Streams, stream)
	}
	if format := details.Format.FormatName; format == "mov" || strings.HasPrefix(format, "mov,") {
		report.Container.FastStart = qcFastStart(session.Path)
	}
	// Audio and video are decoded concurrently; the first failure stops both.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var audioErr, videoErr error
	audioDone := make(chan struct{})
	if audio := info.selectedAudio(); audio != nil {
		go func() {
			defer close(audioDone)
			if report.Audio, audioErr = qcAudio(runCtx, c, session.Path, audio, origin, start, end); audioErr != nil {
				cancel()
			}
		}()
	} else {
		close(audioDone)
	}
	if video := info.selectedVideo(); video != nil {
		rate := ""
		report.Video = &QCVideo{}
		for _, s := range details.Streams {
			if s.Index == video.Index {
				rate, report.Video.RFrameRate, report.Video.AvgFrameRate = s.RFrameRate, s.RFrameRate, s.AvgFrameRate
			}
		}
		if videoErr = qcVideo(runCtx, c, session.Path, video, report.Video, mediaqc.ParseRate(rate), origin, start, end, options); videoErr != nil {
			cancel()
		}
	}
	<-audioDone
	if err = ctx.Err(); err != nil {
		return QCReport{}, err
	}
	for _, err = range []error{videoErr, audioErr} {
		if err != nil && !errors.Is(err, context.Canceled) {
			return QCReport{}, err
		}
	}
	if err = errors.Join(videoErr, audioErr); err != nil {
		return QCReport{}, err
	}
	if v, a := report.Video, report.Audio; v != nil && a != nil && v.FrameCount > 0 {
		report.Sync = &QCSync{StartOffsetMS: a.StartMS - v.FirstFrameMS, EndOffsetMS: a.EndMS - v.EndMS, DurationDeltaMS: a.DurationMS - v.DurationMS}
	}
	if err = analysisUnchanged(session); err != nil {
		return QCReport{}, err
	}
	report.Checks = mediaqc.Evaluate(&report, expectations)
	report.Passed = mediaqc.Passed(report.Checks)
	return report, nil
}

type qcStreamDetails struct {
	Index        int    `json:"index"`
	RFrameRate   string `json:"r_frame_rate"`
	AvgFrameRate string `json:"avg_frame_rate"`
}

type qcFormatDetails struct {
	Format struct {
		FormatName string `json:"format_name"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []qcStreamDetails `json:"streams"`
}

// qcDetails reads the few fields the shared inspection does not request.
func qcDetails(ctx context.Context, c Config, path string) (qcFormatDetails, error) {
	var details qcFormatDetails
	timeout := c.SourceTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-show_entries", "format=format_name,size,bit_rate:stream=index,r_frame_rate,avg_frame_rate", "-of", "json", path)
	data, err := runCommand(ctx, c.FFprobe, args...)
	if err != nil {
		return details, &mediaerror.InspectionFailure{Cause: err}
	}
	if err = json.Unmarshal(data, &details); err != nil || len(details.Streams) > mediainfo.MaxStreams || len(details.Format.FormatName) > 128 {
		return details, analysisMalformed()
	}
	for _, s := range details.Streams {
		if len(s.RFrameRate) > 32 || len(s.AvgFrameRate) > 32 {
			return details, analysisMalformed()
		}
	}
	return details, nil
}

func qcSeconds(value string) (float64, bool) {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil || !finite(seconds) || math.Abs(seconds) > 30*24*3600 {
		return 0, false
	}
	return seconds, true
}

func qcFastStart(path string) *bool {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil
	}
	fast, err := mediaqc.FastStart(file, stat.Size())
	if err != nil {
		return nil
	}
	return &fast
}

func qcVideo(ctx context.Context, c Config, path string, video *probeStream, v *QCVideo, declaredRate, origin float64, start, end int64, options QCOptions) error {
	v.Track, v.Codec, v.CodedWidth, v.CodedHeight = video.Index, video.CodecName, video.Width, video.Height
	v.Rotation, v.SampleAspectRatio, v.PixelFormat = video.Rotation(), video.SampleAspectRatio, video.PixelFormat
	v.Color = QCColor{Range: video.ColorRange, Primaries: video.ColorPrimaries, Transfer: video.ColorTransfer, Matrix: video.ColorSpace}
	if width, height, err := mediainfo.DisplayGeometry(video); err == nil {
		v.Width, v.Height = width, height
	}
	num, den, ok := strings.Cut(video.TimeBase, "/")
	clock := mediaqc.Clock{OriginMS: origin}
	var numErr, denErr error
	clock.Num, numErr = strconv.ParseInt(num, 10, 64)
	clock.Den, denErr = strconv.ParseInt(den, 10, 64)
	if !ok || numErr != nil || denErr != nil || clock.Num <= 0 || clock.Den <= 0 || clock.Num > 1<<31 || clock.Den > 1<<31 {
		return &mediaerror.Problem{Code: "unsupported_stream", Message: "frame timing requires a known video time base"}
	}
	packets := &qcPackets{clock: clock, from: float64(start), until: float64(end), ticks: make([]int64, 0, min(analysisVideoMaxFrames, 1024))}
	args := []string{"-v", "error", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	// Read from the beginning: a seek can land after the window start. The
	// margin keeps reordered frames whose packets follow the end in decode order.
	until := strconv.FormatFloat((origin+float64(end))/1000+1, 'f', 6, 64)
	args = append(args, "-select_streams", strconv.Itoa(video.Index), "-read_intervals", "%"+until, "-show_packets", "-show_entries", "packet=pts,dts,duration,flags", "-of", "compact=p=0:nk=0", path)
	if err := qcRun(ctx, c.FFprobe, args, packets); err != nil {
		return err
	}
	if err := packets.finish(); err != nil {
		return err
	}
	mediaqc.MeasureTiming(v, packets.ticks, clock, packets.lastDuration, end, declaredRate)

	o, err := videoAnalysisOptions(VideoAnalysisOptions{MinBlackMS: options.MinBlackMS})
	if err != nil {
		return err
	}
	parser := newVideoAnalysisParser(start, end, video.Index, o)
	freeze := &qcFreeze{parser: parser, open: -1, events: make([]QCInterval, 0)}
	// metadata prints only frames that carry a key; qc.frame marks every
	// frame so the parser sees an uninterrupted frame sequence.
	parser.skipScene, parser.other = true, freeze.metadata
	filter := fmt.Sprintf("trim=duration=%s,blackdetect=d=0:pix_th=%s:pic_th=%s,freezedetect=n=0.001:d=%s,metadata=mode=add:key=qc.frame:value=1,metadata=mode=print:file='pipe\\:1'", seconds(end-start), strconv.FormatFloat(o.BlackPixelThreshold, 'f', 6, 64), strconv.FormatFloat(o.BlackPictureRatio, 'f', 6, 64), seconds(options.MinFreezeMS))
	args = analysisInputArgs(c, path, start, end)
	args = append(args, "-map", "0:"+strconv.Itoa(video.Index), "-an", "-sn", "-dn", "-vf", filter, "-threads", "1", "-fps_mode", "passthrough", "-f", "null", "-")
	if err = runAnalysis(ctx, c, args, parser); err != nil {
		return err
	}
	if err = parser.finish(); err != nil {
		return err
	}
	if err = freeze.finish(parser.result.AnalyzedEndMS); err != nil {
		return err
	}
	v.AnalyzedEndMS = parser.result.AnalyzedEndMS
	v.Black = make([]QCInterval, 0, len(parser.result.Black))
	for _, b := range parser.result.Black {
		v.Black = append(v.Black, QCInterval{StartMS: b.StartMS, EndMS: b.EndMS})
	}
	tolerance := int64(50)
	if v.IntervalMS.Median > 0 {
		tolerance = int64(math.Ceil(v.IntervalMS.Max))
	}
	v.Freeze = mediaqc.WithoutBlack(freeze.events, v.Black, tolerance)
	return nil
}

// qcFreeze collects freezedetect intervals from the video metadata stream.
// freeze_start names the first frozen frame, so it precedes the frame that
// carries it; freeze_end is the first changed frame.
type qcFreeze struct {
	parser *videoAnalysisParser
	open   int64
	events []QCInterval
}

func (f *qcFreeze) metadata(key, value string) error {
	switch key {
	case "lavfi.freezedetect.freeze_start":
		at, err := f.parser.timestamp(value)
		if err != nil || f.open >= 0 || at > f.parser.atMS {
			return analysisMalformed()
		}
		f.open = at
	case "lavfi.freezedetect.freeze_end":
		at, err := f.parser.timestamp(value)
		if err != nil || f.open < 0 || at < f.open {
			return analysisMalformed()
		}
		return f.close(at)
	}
	return nil
}

func (f *qcFreeze) close(at int64) error {
	if len(f.events) >= AnalysisMaxEvents {
		return analysisLimit("qc exceeds 4096 frozen intervals; choose a shorter interval")
	}
	f.events = append(f.events, QCInterval{StartMS: f.open, EndMS: at})
	f.open = -1
	return nil
}

func (f *qcFreeze) finish(end int64) error {
	if f.open < 0 {
		return nil
	}
	return f.close(max(end, f.open))
}

// qcPackets keeps one timestamp per video packet inside the window, never the
// probe transcript. Discarded packets (edit-list preroll) are not frames.
type qcPackets struct {
	clock        mediaqc.Clock
	from, until  float64
	ticks        []int64
	latest       int64
	lastDuration int64
	line         [qcPacketMaxLine]byte
	lineLength   int
	bytes        int64
}

func (p *qcPackets) Write(data []byte) (int, error) {
	length := len(data)
	if int64(length) > analysisVideoMaxBytes-p.bytes {
		return 0, analysisLimit("qc exceeds its 64 MiB packet listing budget; choose a shorter interval")
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

func (p *qcPackets) parseLine(line string) error {
	if line == "" {
		return nil
	}
	var pts, dts, duration, flags string
	for _, field := range strings.Split(line, "|") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			return analysisMalformed()
		}
		switch key {
		case "pts":
			pts = value
		case "dts":
			dts = value
		case "duration":
			duration = value
		case "flags":
			flags = value
		}
	}
	if strings.Contains(flags, "D") {
		return nil
	}
	if pts == "" || pts == "N/A" {
		pts = dts
	}
	ticks, err := strconv.ParseInt(pts, 10, 64)
	if err != nil {
		return analysisMalformed()
	}
	at := p.clock.MS(ticks)
	if !finite(at) || at < p.from-0.001 || at > p.until-0.001 {
		return nil
	}
	if len(p.ticks) >= analysisVideoMaxFrames {
		return analysisLimit("qc exceeds 180000 video frames; choose a shorter interval")
	}
	if len(p.ticks) == 0 || ticks >= p.latest {
		p.latest, p.lastDuration = ticks, 0
		if value, err := strconv.ParseInt(duration, 10, 64); err == nil && value > 0 {
			p.lastDuration = value
		}
	}
	p.ticks = append(p.ticks, ticks)
	return nil
}

func (p *qcPackets) finish() error {
	if p.lineLength != 0 {
		return analysisMalformed()
	}
	return nil
}

func qcAudio(ctx context.Context, c Config, path string, audio *probeStream, origin float64, start, end int64) (*QCAudio, error) {
	rate, err := strconv.ParseInt(audio.SampleRate, 10, 32)
	if err != nil || rate < 1000 || rate > 768000 {
		return nil, &mediaerror.Problem{Code: "unsupported_stream", Message: "qc requires a known audio sample rate"}
	}
	if audio.Channels < 1 || audio.Channels > 32 {
		return nil, analysisArgument("qc supports 1 through 32 audio channels")
	}
	a := &QCAudio{Track: audio.Index, Codec: audio.CodecName, SampleRate: int(rate), Channels: audio.Channels, StartMS: start}
	if value, ok := qcSeconds(audio.StartTime); ok {
		a.StartMS = max(start, int64(math.Round(value*1000-origin)))
	}
	limit := ((end-start)*rate+999)/1000 + rate
	if limit > analysisAudioMaxSamples/int64(audio.Channels) {
		return nil, analysisLimit("qc exceeds its 256 million decoded sample budget; choose a shorter interval")
	}
	pcm := &qcPCM{channels: int64(audio.Channels), limit: limit * int64(audio.Channels), first: -1, last: -1}
	args := analysisInputArgs(c, path, start, end)
	args = append(args, "-map", "0:"+strconv.Itoa(audio.Index), "-vn", "-sn", "-dn", "-threads", "1", "-ac", strconv.Itoa(audio.Channels), "-ar", strconv.FormatInt(rate, 10), "-c:a", "pcm_f32le", "-f", "f32le", "pipe:1")
	if err = runAnalysis(ctx, c, args, pcm); err != nil {
		return nil, err
	}
	if pcm.partialCount != 0 || pcm.samples%pcm.channels != 0 {
		return nil, analysisMalformed()
	}
	frames := pcm.samples / pcm.channels
	toMS := func(frames int64) int64 { return int64(math.Round(float64(frames) * 1000 / float64(rate))) }
	a.DurationMS = toMS(frames)
	a.EndMS = min(end, a.StartMS+a.DurationMS)
	a.ClippedSamples = pcm.clipped
	if pcm.peak > 0 {
		peak := math.Round(20*math.Log10(pcm.peak)*100) / 100
		a.PeakDBFS = &peak
	}
	a.LeadingSilenceMS, a.TrailingSilenceMS = a.DurationMS, a.DurationMS
	if pcm.first >= 0 {
		a.LeadingSilenceMS, a.TrailingSilenceMS = toMS(pcm.first), toMS(frames-pcm.last-1)
	}
	if frames > 0 && audio.Channels <= 8 {
		track := audio.Index
		loudness, err := AnalyzeLoudness(ctx, c, path, LoudnessOptions{StartMS: start, EndMS: end, AudioTrack: &track})
		if err != nil {
			return nil, err
		}
		a.IntegratedLUFS, a.TruePeakDBTP, a.LoudnessRangeLU = loudness.IntegratedLUFS, loudness.TruePeakDBTP, loudness.LoudnessRangeLU
	}
	return a, nil
}

// qcPCM measures interleaved float samples without retaining them.
type qcPCM struct {
	channels, limit, samples int64
	clipped                  int64
	peak                     float64
	first, last              int64
	partial                  [4]byte
	partialCount             int
}

func (p *qcPCM) Write(data []byte) (int, error) {
	length := len(data)
	for len(data) > 0 {
		var bits uint32
		if p.partialCount != 0 || len(data) < 4 {
			n := copy(p.partial[p.partialCount:], data)
			p.partialCount += n
			data = data[n:]
			if p.partialCount != 4 {
				continue
			}
			bits, p.partialCount = binary.LittleEndian.Uint32(p.partial[:]), 0
		} else {
			bits, data = binary.LittleEndian.Uint32(data), data[4:]
		}
		if p.samples >= p.limit {
			return 0, analysisLimit("audio decoder exceeded the requested interval")
		}
		value := math.Abs(float64(math.Float32frombits(bits)))
		if !finite(value) {
			return 0, analysisMalformed()
		}
		if value > 1 {
			p.clipped++
		}
		if value > p.peak {
			p.peak = value
		}
		if value > mediaqc.SilenceThreshold {
			frame := p.samples / p.channels
			if p.first < 0 {
				p.first = frame
			}
			p.last = frame
		}
		p.samples++
	}
	return length, nil
}

func qcRun(ctx context.Context, tool string, args []string, writer io.Writer) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &analysisOutput{writer: writer, cancel: cancel}
	err := runCommandOutput(runCtx, tool, args, output)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if output.err != nil {
		return output.err
	}
	return err
}
