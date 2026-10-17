package subtitles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

const (
	maxSuppliedSubtitleBytes       = 512 << 10
	maxSidecarBytes                = 8 << 20
	maxSubtitleClockMS       int64 = 720 * 3600000
)

var subtitleBlockBreak = regexp.MustCompile(`\n[\t ]*\n`)
var subtitleFullClock = regexp.MustCompile(`^([0-9]{2,3}):([0-5][0-9]):([0-5][0-9])[.,]([0-9]{3})$`)
var subtitleShortClock = regexp.MustCompile(`^([0-5][0-9]):([0-5][0-9])\.([0-9]{3})$`)
var subtitleInlineClock = regexp.MustCompile(`<([0-9]{2,}:)?[0-9]{2}:[0-9]{2}\.[0-9]{3}>`)
var subtitleNumber = regexp.MustCompile(`^[0-9]+$`)
var subtitleVTTOnlyTag = regexp.MustCompile(`</?(v|c|lang|ruby|rt)([ .>])`)

func validSubtitleSetting(key, value string) bool {
	base, alignment, aligned := strings.Cut(value, ",")
	percentage := func(raw string) bool {
		if !strings.HasSuffix(raw, "%") {
			return false
		}
		raw = strings.TrimSuffix(raw, "%")
		if raw == "" || strings.ContainsAny(raw, "+-eE") {
			return false
		}
		n, err := strconv.ParseFloat(raw, 64)
		return err == nil && n >= 0 && n <= 100
	}
	switch key {
	case "vertical":
		return !aligned && (base == "rl" || base == "lr")
	case "align":
		return !aligned && (base == "start" || base == "center" || base == "end" || base == "left" || base == "right")
	case "size":
		return !aligned && percentage(base)
	case "position":
		return percentage(base) && (!aligned || alignment == "line-left" || alignment == "center" || alignment == "line-right" || alignment == "auto")
	case "line":
		_, integer := strconv.ParseInt(base, 10, 32)
		return (base == "auto" || percentage(base) || integer == nil) && (!aligned || alignment == "start" || alignment == "center" || alignment == "end")
	}
	return false
}

type suppliedSubtitleCue struct {
	Cue
	settings string
}

func subtitleInvalid(code, field string) error {
	return &RetimeError{Code: code, Field: field}
}

func suppliedSubtitleClock(value, format string) (int64, error) {
	parts := subtitleFullClock.FindStringSubmatch(value)
	if format == "vtt" && strings.Contains(value, ",") {
		return 0, subtitleInvalid("invalid_subtitles", "text")
	}
	var hours, minutes, seconds, millis int64
	if len(parts) == 5 {
		hours, _ = strconv.ParseInt(parts[1], 10, 64)
		minutes, _ = strconv.ParseInt(parts[2], 10, 64)
		seconds, _ = strconv.ParseInt(parts[3], 10, 64)
		millis, _ = strconv.ParseInt(parts[4], 10, 64)
	} else if parts = subtitleShortClock.FindStringSubmatch(value); format == "vtt" && len(parts) == 4 {
		minutes, _ = strconv.ParseInt(parts[1], 10, 64)
		seconds, _ = strconv.ParseInt(parts[2], 10, 64)
		millis, _ = strconv.ParseInt(parts[3], 10, 64)
	} else {
		return 0, subtitleInvalid("invalid_subtitles", "text")
	}
	result := hours*3600000 + minutes*60000 + seconds*1000 + millis
	if result > maxSubtitleClockMS {
		return 0, subtitleInvalid("subtitle_limit", "text")
	}
	return result, nil
}

func parseSuppliedSubtitles(ctx context.Context, request RetimeRequest) ([]suppliedSubtitleCue, error) {
	if len(request.Text) > maxSuppliedSubtitleBytes {
		return nil, subtitleInvalid("subtitle_limit", "text")
	}
	if !utf8.ValidString(request.Text) || strings.ContainsRune(request.Text, 0) {
		return nil, subtitleInvalid("invalid_subtitles", "text")
	}
	raw := strings.TrimPrefix(request.Text, "\ufeff")
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	blocks := subtitleBlockBreak.Split(strings.TrimSpace(raw), -1)
	if request.Format == "vtt" {
		header := strings.Split(blocks[0], "\n")
		if !(header[0] == "WEBVTT" || strings.HasPrefix(header[0], "WEBVTT ") || strings.HasPrefix(header[0], "WEBVTT\t")) || strings.Contains(header[0], "-->") {
			return nil, subtitleInvalid("invalid_subtitles", "text")
		}
		// External clocks/styles must never silently survive a timeline change.
		if len(header) != 1 {
			return nil, subtitleInvalid("subtitle_unsupported", "text")
		}
		blocks = blocks[1:]
	} else if strings.TrimSpace(raw) == "" {
		blocks = nil
	}
	cues := make([]suppliedSubtitleCue, 0, min(len(blocks), 10000))
	for _, block := range blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines := strings.Split(strings.TrimSpace(block), "\n")
		if request.Format == "vtt" {
			if lines[0] == "NOTE" || strings.HasPrefix(lines[0], "NOTE ") || strings.HasPrefix(lines[0], "NOTE\t") {
				continue
			}
			if lines[0] == "STYLE" || lines[0] == "REGION" || strings.HasPrefix(lines[0], "X-TIMESTAMP-MAP") {
				return nil, subtitleInvalid("subtitle_unsupported", "text")
			}
		}
		position := 0
		if !strings.Contains(lines[0], "-->") {
			position = 1
			if request.Format == "srt" && !subtitleNumber.MatchString(lines[0]) {
				return nil, subtitleInvalid("invalid_subtitles", "text")
			}
		}
		if len(lines) <= position+1 {
			return nil, subtitleInvalid("invalid_subtitles", "text")
		}
		timing := strings.Fields(lines[position])
		if len(timing) < 3 || timing[1] != "-->" {
			return nil, subtitleInvalid("invalid_subtitles", "text")
		}
		start, err := suppliedSubtitleClock(timing[0], request.Format)
		if err != nil {
			return nil, err
		}
		end, err := suppliedSubtitleClock(timing[2], request.Format)
		if err != nil {
			return nil, err
		}
		if end <= start {
			return nil, subtitleInvalid("invalid_subtitles", "text")
		}
		settings := ""
		if len(timing) > 3 {
			if request.Format != "vtt" || request.OutputFormat == "srt" || len(timing) > 9 {
				return nil, subtitleInvalid("subtitle_unsupported", "text")
			}
			seen := map[string]bool{}
			for _, field := range timing[3:] {
				key, value, found := strings.Cut(field, ":")
				if !found || len(value) > 64 || seen[key] || !validSubtitleSetting(key, value) {
					return nil, subtitleInvalid("subtitle_unsupported", "text")
				}
				seen[key] = true
			}
			settings = " " + strings.Join(timing[3:], " ")
		}
		text := strings.Join(lines[position+1:], "\n")
		if strings.Contains(text, "-->") || subtitleInlineClock.MatchString(text) {
			return nil, subtitleInvalid("subtitle_unsupported", "text")
		}
		if request.Format == "vtt" && request.OutputFormat == "srt" && subtitleVTTOnlyTag.MatchString(text) {
			return nil, subtitleInvalid("subtitle_unsupported", "text")
		}
		if len(cues) >= 10000 {
			return nil, subtitleInvalid("subtitle_limit", "text")
		}
		cues = append(cues, suppliedSubtitleCue{Cue{start, end, text}, settings})
	}
	// Deterministic chronological order also permits indexed overlap searches.
	sort.SliceStable(cues, func(i, j int) bool { return cues[i].StartMS < cues[j].StartMS })
	return cues, nil
}

// Retime produces bounded UTF-8 sidecars using actual export maps.
// Intervals are half-open. Source cuts may repeat or be reordered; output maps
// must be ordered and non-overlapping. Clocks are translated, never stretched.
func Retime(ctx context.Context, request RetimeRequest) (RetimeResult, error) {
	result := RetimeResult{Version: 1, Outputs: []Sidecar{}}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if request.Version != 1 || (request.Format != "srt" && request.Format != "vtt") {
		return result, subtitleInvalid("invalid_subtitles", "version/format")
	}
	if request.OutputFormat == "" {
		request.OutputFormat = request.Format
	}
	if request.OutputFormat != "srt" && request.OutputFormat != "vtt" {
		return result, subtitleInvalid("invalid_subtitles", "output_format")
	}
	if len(request.Outputs) == 0 || len(request.Outputs) > 128 {
		return result, subtitleInvalid("subtitle_limit", "outputs")
	}
	totalSegments := 0
	seen := make(map[string]bool)
	for i, clip := range request.Outputs {
		field := fmt.Sprintf("outputs[%d]", i)
		if !edit.ValidCollectionID(clip.ClipID) || seen[clip.ClipID] {
			return result, subtitleInvalid("invalid_segment_map", field+".clip_id")
		}
		seen[clip.ClipID] = true
		totalSegments += len(clip.Segments)
		if len(clip.Segments) == 0 || len(clip.Segments) > 128 || totalSegments > 4096 {
			return result, subtitleInvalid("subtitle_limit", field+".segments")
		}
		var previousEnd int64
		for j, segment := range clip.Segments {
			if segment.SourceStartMS < 0 || segment.SourceEndMS <= segment.SourceStartMS || segment.SourceEndMS > maxSubtitleClockMS || segment.OutputStartMS < previousEnd || segment.OutputEndMS <= segment.OutputStartMS || segment.OutputEndMS > maxSubtitleClockMS {
				return result, subtitleInvalid("invalid_segment_map", fmt.Sprintf("%s.segments[%d]", field, j))
			}
			previousEnd = segment.OutputEndMS
		}
	}
	cues, err := parseSuppliedSubtitles(ctx, request)
	if err != nil {
		return result, err
	}
	baseCues := make([]Cue, len(cues))
	for i := range cues {
		baseCues[i] = cues[i].Cue
	}
	index := NewIndex(baseCues)
	totalBytes, totalCues, visits := 0, 0, 0
	for _, clip := range request.Outputs {
		var out strings.Builder
		if request.OutputFormat == "vtt" {
			out.WriteString("WEBVTT\n\n")
		}
		count := 0
		for _, segment := range clip.Segments {
			if err := ctx.Err(); err != nil {
				return RetimeResult{}, err
			}
			first := sort.Search(len(cues), func(k int) bool { return index.MaxEnd[k] > segment.SourceStartMS })
			last := sort.Search(len(cues), func(k int) bool { return cues[k].StartMS >= segment.SourceEndMS })
			for k := first; k < last; k++ {
				cue := cues[k]
				visits++
				if visits > 1000000 {
					return RetimeResult{}, subtitleInvalid("subtitle_limit", "outputs")
				}
				start, end := max(cue.StartMS, segment.SourceStartMS), min(cue.EndMS, segment.SourceEndMS)
				if end <= start {
					continue
				}
				start = segment.OutputStartMS + start - segment.SourceStartMS
				end = min(segment.OutputEndMS, segment.OutputStartMS+end-segment.SourceStartMS)
				if end <= start {
					continue
				}
				if totalBytes+out.Len()+len(cue.Text)+200 > maxSidecarBytes || totalCues >= 100000 {
					return RetimeResult{}, subtitleInvalid("subtitle_limit", "outputs")
				}
				count++
				totalCues++
				startTime, endTime := Timestamp(start), Timestamp(end)
				settings := ""
				if request.OutputFormat == "vtt" {
					startTime, endTime = strings.ReplaceAll(startTime, ",", "."), strings.ReplaceAll(endTime, ",", ".")
					settings = cue.settings
				}
				fmt.Fprintf(&out, "%d\n%s --> %s%s\n%s\n\n", count, startTime, endTime, settings, cue.Text)
			}
		}
		text := out.String()
		totalBytes += len(text)
		digest := sha256.Sum256([]byte(text))
		result.Outputs = append(result.Outputs, Sidecar{ClipID: clip.ClipID, Format: request.OutputFormat, Text: text, CueCount: count, SHA256: hex.EncodeToString(digest[:])})
	}
	return result, nil
}
