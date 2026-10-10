package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

const maxManifestBytes int64 = 2 << 20
const maxHLSSegments = 100000

type hlsSegment struct {
	URL            string
	StartMS, EndMS int64
	MapURL         string
	Discontinuity  bool
}
type hlsVariant struct {
	URL           string
	Height        int
	Bandwidth     int64
	ExternalAudio bool
}
type hlsPlaylist struct {
	Segments   []hlsSegment
	Variants   []hlsVariant
	DurationMS int64
}

func isHLS(f platformFormat) bool { return f.Protocol == "m3u8_native" || f.Protocol == "m3u8" }

// No playlist is passed to FFmpeg. Each URI is parsed here and fetched through
// the same pinned public-IP client, with a shared byte budget and job context.
func resolveHLSURL(base, reference string) (string, error) {
	b, e := validateURL(base)
	if e != nil {
		return "", errUnsupportedStream
	}
	return resolveHLSReference(b, reference)
}

func resolveHLSReference(base *url.URL, reference string) (string, error) {
	r, e := url.Parse(reference)
	if e != nil || r.Opaque != "" {
		return "", errUnsupportedStream
	}
	u := base.ResolveReference(r)
	if e = validateParsedURL(u); e != nil {
		return "", errUnsupportedStream
	}
	return u.String(), nil
}
func hlsAttributes(text string) (map[string]string, error) {
	values := map[string]string{}
	for text != "" {
		key, tail, ok := strings.Cut(text, "=")
		if !ok || key == "" {
			return nil, errUnsupportedStream
		}
		var value string
		if strings.HasPrefix(tail, "\"") {
			end := strings.Index(tail[1:], "\"")
			if end < 0 {
				return nil, errUnsupportedStream
			}
			value = tail[1 : 1+end]
			text = tail[end+2:]
			if text != "" {
				if text[0] != ',' {
					return nil, errUnsupportedStream
				}
				text = text[1:]
			}
		} else {
			value, text, _ = strings.Cut(tail, ",")
		}
		if _, exists := values[key]; exists {
			return nil, errUnsupportedStream
		}
		values[key] = value
	}
	return values, nil
}

func parseHLS(base string, data []byte) (hlsPlaylist, error) {
	var p hlsPlaylist
	header, _, _ := bytes.Cut(data, []byte("\n"))
	if len(data) > int(maxManifestBytes) || string(bytes.TrimSuffix(header, []byte("\r"))) != "#EXTM3U" {
		return p, errUnsupportedStream
	}
	baseURL, err := validateURL(base)
	if err != nil {
		return p, errUnsupportedStream
	}
	// Size once from actual duration tags; repeated slice growth otherwise
	// copies megabytes for long recordings. The existing segment bound remains.
	capacity := min(bytes.Count(data, []byte("\n#EXTINF:")), maxHLSSegments)
	p.Segments = make([]hlsSegment, 0, capacity)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 64<<10)
	var duration float64
	var nextDuration float64
	var variant map[string]string
	var mapURL string
	endList, discontinuity := false, false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		switch {
		case line == "#EXTM3U":
		case line == "#EXT-X-ENDLIST":
			endList = true
		case line == "#EXT-X-DISCONTINUITY":
			discontinuity = true
		case strings.HasPrefix(line, "#EXT-X-KEY:") || strings.HasPrefix(line, "#EXT-X-SESSION-KEY:"):
			_, raw, _ := strings.Cut(line, ":")
			a, e := hlsAttributes(raw)
			if e != nil || a["METHOD"] != "NONE" {
				return p, errUnsupportedStream
			}
		case strings.HasPrefix(line, "#EXT-X-BYTERANGE:") || strings.HasPrefix(line, "#EXT-X-PART:") || strings.HasPrefix(line, "#EXT-X-GAP") || strings.HasPrefix(line, "#EXT-X-I-FRAMES-ONLY"):
			return p, errUnsupportedStream
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			a, e := hlsAttributes(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if e != nil || a["BYTERANGE"] != "" || a["URI"] == "" {
				return p, errUnsupportedStream
			}
			mapURL, e = resolveHLSReference(baseURL, a["URI"])
			if e != nil {
				return p, e
			}
		case strings.HasPrefix(line, "#EXT-X-STREAM-INF:"):
			if variant != nil || nextDuration != 0 {
				return p, errUnsupportedStream
			}
			var e error
			variant, e = hlsAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if e != nil {
				return p, e
			}
		case strings.HasPrefix(line, "#EXTINF:"):
			if nextDuration != 0 || variant != nil {
				return p, errUnsupportedStream
			}
			value, _, _ := strings.Cut(strings.TrimPrefix(line, "#EXTINF:"), ",")
			var e error
			nextDuration, e = strconv.ParseFloat(value, 64)
			if e != nil || math.IsInf(nextDuration, 0) || math.IsNaN(nextDuration) || nextDuration <= 0 || nextDuration > 300 {
				return p, errUnsupportedStream
			}
		case strings.HasPrefix(line, "#"):
		default:
			u, e := resolveHLSReference(baseURL, line)
			if e != nil {
				return p, e
			}
			if variant != nil {
				v := hlsVariant{URL: u, ExternalAudio: variant["AUDIO"] != ""}
				v.Bandwidth, _ = strconv.ParseInt(variant["BANDWIDTH"], 10, 64)
				_, h, _ := strings.Cut(variant["RESOLUTION"], "x")
				v.Height, _ = strconv.Atoi(h)
				p.Variants = append(p.Variants, v)
				variant = nil
				if len(p.Variants) > 100 {
					return p, errUnsupportedStream
				}
			} else {
				if nextDuration == 0 || endList || len(p.Segments) >= maxHLSSegments {
					return p, errUnsupportedStream
				}
				start := int64(math.Round(duration * 1000))
				duration += nextDuration
				if duration > 30*24*3600 {
					return p, errUnsupportedStream
				}
				end := int64(math.Round(duration * 1000))
				if end <= start {
					return p, errUnsupportedStream
				}
				p.Segments = append(p.Segments, hlsSegment{URL: u, StartMS: start, EndMS: end, MapURL: mapURL, Discontinuity: discontinuity})
				nextDuration = 0
				discontinuity = false
			}
		}
	}
	if scanner.Err() != nil || nextDuration != 0 || variant != nil || len(p.Segments) > 0 && len(p.Variants) > 0 {
		return p, errUnsupportedStream
	}
	if len(p.Variants) > 0 {
		return p, nil
	}
	if !endList {
		return p, errLiveSource
	}
	if len(p.Segments) == 0 {
		return p, errUnsupportedStream
	}
	p.DurationMS = int64(math.Round(duration * 1000))
	return p, nil
}

func mediaHeaders(req *http.Request, headers map[string]string) {
	req.Header.Set("User-Agent", "cutmyvideo-core/0.1")
	for _, key := range []string{"User-Agent", "Referer", "Origin"} {
		if value := headers[key]; value != "" && len(value) < 4096 && !strings.ContainsAny(value, "\r\n") {
			if key != "User-Agent" {
				if _, err := validateURL(value); err != nil {
					continue
				}
			}
			req.Header.Set(key, value)
		}
	}
}
func (g *networkGuard) fetch(ctx context.Context, raw string, headers map[string]string, dst io.Writer, limit int64) error {
	if limit < 0 {
		return errors.New("invalid source transfer limit")
	}
	u, e := validateURL(raw)
	if e != nil {
		return errUnsupportedStream
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return errUnsupportedStream
	}
	mediaHeaders(req, headers)
	resp, e := g.client.Do(req)
	if e != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errPlatformUnavailable
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return &sourceProblem{"media_upstream_denied", "The video service refused the media request; reopen the source or upload a file"}
	}
	if resp.StatusCode != http.StatusOK || resp.ContentLength > limit {
		return errPlatformUnavailable
	}
	var reader io.Reader = resp.Body
	if limit < math.MaxInt64 {
		reader = io.LimitReader(resp.Body, limit+1)
	}
	pooled := relayBuffers.Get().(*[relayBufferSize]byte)
	defer relayBuffers.Put(pooled)
	buffer := pooled[:]
	var total int64
	for {
		n, readErr := reader.Read(buffer)
		if n > 0 {
			total += int64(n)
			if total > limit || g.bytes.Add(int64(n)) > g.limit {
				return errors.New("source transfer budget exceeded")
			}
			if _, e = dst.Write(buffer[:n]); e != nil {
				return e
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return errPlatformUnavailable
		}
	}
}
func loadHLS(ctx context.Context, g *networkGuard, f platformFormat, quality string, depth int) (hlsPlaylist, error) {
	if depth > 2 {
		return hlsPlaylist{}, errUnsupportedStream
	}
	var data bytes.Buffer
	if e := g.fetch(ctx, f.URL, f.Headers, &data, maxManifestBytes); e != nil {
		return hlsPlaylist{}, e
	}
	p, e := parseHLS(f.URL, data.Bytes())
	if e != nil {
		return p, e
	}
	if len(p.Variants) == 0 {
		return p, nil
	}
	cap := 100000
	if quality == "480p" {
		cap = 480
	} else if quality == "720p" {
		cap = 720
	} else if quality == "1080p" {
		cap = 1080
	}
	var chosen *hlsVariant
	for i := range p.Variants {
		v := &p.Variants[i]
		if v.ExternalAudio || v.Height > cap {
			continue
		}
		if chosen == nil || v.Height > chosen.Height || v.Height == chosen.Height && v.Bandwidth > chosen.Bandwidth {
			chosen = v
		}
	}
	if chosen == nil {
		for i := range p.Variants {
			v := &p.Variants[i]
			if v.ExternalAudio {
				continue
			}
			if chosen == nil || v.Height < chosen.Height || v.Height == chosen.Height && v.Bandwidth < chosen.Bandwidth {
				chosen = v
			}
		}
	}
	if chosen == nil {
		return p, errUnsupportedStream
	}
	f.URL = chosen.URL
	return loadHLS(ctx, g, f, quality, depth+1)
}
func selectHLSSegments(p hlsPlaylist, r Range) ([]hlsSegment, error) {
	if r.StartMS < 0 || r.EndMS <= r.StartMS || r.EndMS > p.DurationMS+1000 {
		return nil, errUnsupportedStream
	}
	// parseHLS constructs strictly increasing, contiguous intervals. Locate
	// just the requested window, rather than scanning hours of other segments.
	first := sort.Search(len(p.Segments), func(i int) bool { return p.Segments[i].EndMS > r.StartMS })
	end := sort.Search(len(p.Segments), func(i int) bool { return p.Segments[i].StartMS >= r.EndMS })
	if first >= end {
		return nil, errUnsupportedStream
	}
	// One preceding segment provides decoder/keyframe context. It is excluded
	// from the requested cut by an explicit global-to-local timeline offset.
	if first > 0 && !p.Segments[first].Discontinuity {
		first--
	}
	return p.Segments[first:end], nil
}

// Staged data contains media bytes only, never untrusted playlist references.
// A local remux normalizes container timestamps before the common export path.
func stageHLS(ctx context.Context, c Config, g *networkGuard, f platformFormat, p hlsPlaylist, r Range, dir string, index int) (string, int64, error) {
	return stageHLSProgress(ctx, c, g, f, p, r, dir, index, nil)
}

func stageHLSProgress(ctx context.Context, c Config, g *networkGuard, f platformFormat, p hlsPlaylist, r Range, dir string, index int, progress func(int, int)) (string, int64, error) {
	segments, e := selectHLSSegments(p, r)
	if e != nil {
		return "", 0, e
	}
	var downloaded atomic.Int64
	groups, e := splitHLSContinuity(segments)
	if e != nil {
		return "", 0, e
	}
	limit := remoteSourceBudget(c)
	if len(groups) == 1 {
		return stageHLSGroup(ctx, c, g, f, segments, dir, index, &downloaded, limit, limit, progress)
	}
	return stageHLSGroups(ctx, c, g, f, groups, dir, index, &downloaded, limit, progress)
}

func stageHLSGroup(ctx context.Context, c Config, g *networkGuard, f platformFormat, segments []hlsSegment, dir string, index int, downloaded *atomic.Int64, limit, muxLimit int64, progress func(int, int)) (string, int64, error) {
	var e error
	raw := filepath.Join(dir, fmt.Sprintf("stream-%d.media", index))
	out := filepath.Join(dir, fmt.Sprintf("stream-%d.mkv", index))
	file, e := os.OpenFile(raw, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return "", 0, e
	}
	defer os.Remove(raw)
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(out)
		}
	}()
	if segments[0].MapURL != "" {
		e = g.fetch(ctx, segments[0].MapURL, f.Headers, &stagingWriter{writer: file, downloaded: downloaded, limit: limit}, limit)
	}
	if e == nil {
		e = fetchHLSSegments(ctx, g, f, segments, dir, index, file, downloaded, limit, progress)
	}
	closeErr := file.Close()
	if e != nil {
		return "", 0, e
	}
	if closeErr != nil {
		return "", 0, closeErr
	}
	// Restrict protocols/demuxers even after staging. HLS, concat and arbitrary
	// network or file references are not accepted by FFmpeg.
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-max_alloc", "268435456", "-protocol_whitelist", "file", "-format_whitelist", mediaFormats}
	args = append(args, mediaInputBounds(c)...)
	args = append(args, "-fflags", "+genpts", "-i", raw, "-map", "0:v:0?", "-map", "0:a:0?", "-c", "copy", "-avoid_negative_ts", "make_zero", "-map_metadata", "-1", "-fs", strconv.FormatInt(muxLimit, 10), out)
	_, e = runCommand(ctx, c.FFmpeg, args...)
	if e != nil {
		return "", 0, e
	}
	info, duration, e := probe(ctx, c, out, false)
	expected := segments[len(segments)-1].EndMS - segments[0].StartMS
	if e != nil || math.Abs(float64(duration-expected)) > 1000 {
		return "", 0, errUnsupportedStream
	}
	stat, e := os.Stat(out)
	if e != nil || stat.Size() >= muxLimit {
		return "", 0, errUnsupportedStream
	}
	// make_zero shifts decoding timestamps (including B-frame reordering).
	// EXTINF's timeline is anchored to the first video presentation timestamp,
	// not the muxer's earliest audio/DTS. Preserve that relation explicitly.
	anchor := math.NaN()
	for _, stream := range info.Streams {
		if stream.CodecType == "video" {
			value, err := strconv.ParseFloat(stream.StartTime, 64)
			if err != nil {
				return "", 0, errUnsupportedStream
			}
			anchor = value
			break
		}
	}
	if math.IsNaN(anchor) {
		for _, stream := range info.Streams {
			if stream.CodecType == "audio" {
				value, err := strconv.ParseFloat(stream.StartTime, 64)
				if err != nil {
					return "", 0, errUnsupportedStream
				}
				anchor = value
				break
			}
		}
	}
	if math.IsNaN(anchor) || math.IsInf(anchor, 0) || anchor < 0 || anchor > 2 {
		return "", 0, errUnsupportedStream
	}
	keep = true
	return out, segments[0].StartMS - int64(math.Round(anchor*1000)), nil
}

type stagingWriter struct {
	writer     io.Writer
	downloaded *atomic.Int64
	limit      int64
}

func (w *stagingWriter) Write(data []byte) (int, error) {
	if w.downloaded.Add(int64(len(data))) > w.limit {
		return 0, errors.New("source transfer budget exceeded")
	}
	return w.writer.Write(data)
}

// Four bounded downloads hide per-segment latency without buffering media in
// RAM. Files are appended in playlist order and removed after each batch. Raw
// plus pending segments never exceeds twice the enforced staging allowance;
// all segment files are gone before remuxing creates the second full copy.
func fetchHLSSegments(ctx context.Context, g *networkGuard, format platformFormat, segments []hlsSegment, dir string, stream int, output io.Writer, downloaded *atomic.Int64, limit int64, progress func(int, int)) error {
	const concurrency = 4
	for first := 0; first < len(segments); first += concurrency {
		count := min(concurrency, len(segments)-first)
		batchCtx, cancel := context.WithCancel(ctx)
		paths := make([]string, count)
		failures := make([]error, count)
		var workers sync.WaitGroup
		for slot := range count {
			paths[slot] = filepath.Join(dir, fmt.Sprintf("segment-%d-%d.part", stream, first+slot))
			workers.Add(1)
			go func(slot int) {
				defer workers.Done()
				file, err := os.OpenFile(paths[slot], os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
				if err == nil {
					err = g.fetch(batchCtx, segments[first+slot].URL, format.Headers, &stagingWriter{writer: file, downloaded: downloaded, limit: limit}, limit)
					closeErr := file.Close()
					if err == nil {
						err = closeErr
					}
				}
				failures[slot] = err
				if err != nil {
					cancel()
				}
			}(slot)
		}
		workers.Wait()
		cancel()
		var failure error
		for _, err := range failures {
			if err != nil && (failure == nil || errors.Is(failure, context.Canceled)) {
				failure = err
			}
		}
		for slot, path := range paths {
			if failure == nil {
				file, err := os.Open(path)
				if err == nil {
					_, err = io.Copy(output, file)
					closeErr := file.Close()
					if err == nil {
						err = closeErr
					}
				}
				failure = err
				if failure == nil && progress != nil {
					progress(first+slot+1, len(segments))
				}
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) && failure == nil {
				failure = err
			}
		}
		if failure != nil {
			return failure
		}
	}
	return nil
}
