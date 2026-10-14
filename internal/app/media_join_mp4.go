package app

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
)

const ownedMP4MaxFileBytes = 64 << 20
const ownedMP4MaxMoovBytes = 1 << 20
const ownedMP4MaxReadBytes = 8 << 20
const ownedMP4MaxSamples = 32768

// Only the accurate join's successful ultrafast H.264/AAC producer uses this
// fresh structural inspection. Source/copy/compact/unknown envelopes retain
// FFprobe. This checks container/sample/NAL structure, not arbitrary decoded
// content; independent complete decode and clock gates verify the fast path.
func verifyJoinedOwnedMP4(ctx context.Context, c Config, path string, request ExportRequest, expected, limit int64, selection mediaSelection) (probeInfo, error) {
	if selection.mp3DurationMode == ownedMP3DurationUnknown {
		return verifyJoined(ctx, c, path, request, expected, limit)
	}
	before, err := os.Stat(path)
	if err != nil {
		return probeInfo{}, err
	}
	if before.Size() >= limit {
		return probeInfo{}, errOutputLimit
	}
	width, height, err := videoOutputGeometryWithCrop(selection.Video, request.Quality, false, request.Crop)
	if err != nil {
		return probeInfo{}, err
	}
	p, duration, supported, err := probeOwnedMP4(ctx, path, before)
	if err != nil {
		return probeInfo{}, err
	}
	video, audio := p.selectedVideo(), p.selectedAudio()
	if !supported || p.validateStreams() != nil || video == nil || video.Width != width || video.Height != height || (audio == nil) != (selection.Audio == nil) {
		return verifyJoined(ctx, c, path, request, expected, limit)
	}
	if math.Abs(float64(duration-expected)) > 350 {
		return p, fmt.Errorf("joined result duration does not match its selected intervals")
	}
	return p, nil
}

type mp4Box struct {
	kind string
	data []byte
}
type mp4Span struct{ start, end int64 }
type ownedMP4Reader struct {
	ctx        context.Context
	file       io.ReaderAt
	size, read int64
}

func (r *ownedMP4Reader) bytes(at int64, n int) ([]byte, bool) {
	if r.ctx.Err() != nil || n < 0 || at < 0 || int64(n) > r.size-at || int64(n) > ownedMP4MaxReadBytes-r.read {
		return nil, false
	}
	r.read += int64(n)
	b := make([]byte, n)
	_, err := r.file.ReadAt(b, at)
	return b, err == nil
}

// Only bounded, ordinary boxes are admitted. Extended/fragmented/unknown
// layouts fall back rather than guessing their offsets or duration semantics.
func mp4Boxes(data []byte) ([]mp4Box, bool) {
	var boxes []mp4Box
	for len(data) > 0 {
		if len(data) < 8 || len(boxes) >= 64 {
			return nil, false
		}
		n := int64(binary.BigEndian.Uint32(data))
		if n < 8 || n > int64(len(data)) {
			return nil, false
		}
		boxes = append(boxes, mp4Box{string(data[4:8]), data[8:n]})
		data = data[n:]
	}
	return boxes, true
}

func mp4Child(data []byte, kind string) ([]byte, bool) {
	boxes, ok := mp4Boxes(data)
	if !ok {
		return nil, false
	}
	var found []byte
	for _, box := range boxes {
		if box.kind == kind {
			if found != nil {
				return nil, false
			}
			found = box.data
		}
	}
	return found, found != nil
}

func probeOwnedMP4(ctx context.Context, path string, before os.FileInfo) (p probeInfo, duration int64, supported bool, err error) {
	if err := ctx.Err(); err != nil {
		return p, 0, false, err
	}
	if before == nil || !before.Mode().IsRegular() || before.Size() <= 0 || before.Size() > ownedMP4MaxFileBytes {
		return p, 0, false, nil
	}
	entry, e := os.Lstat(path)
	if e != nil || !sameOwnedMP3File(before, entry) {
		return p, 0, false, nil
	}
	f, e := os.Open(path)
	if e != nil {
		return p, 0, false, nil
	}
	defer f.Close()
	opened, e := f.Stat()
	if e != nil || !sameOwnedMP3File(before, opened) {
		return p, 0, false, nil
	}
	r := ownedMP4Reader{ctx: ctx, file: f, size: opened.Size()}
	p, duration, supported = r.inspect()
	after, e := f.Stat()
	current, pe := os.Lstat(path)
	if e != nil || pe != nil || !sameOwnedMP3File(opened, after) || !sameOwnedMP3File(opened, current) {
		return probeInfo{}, 0, false, nil
	}
	if err := ctx.Err(); err != nil {
		return probeInfo{}, 0, false, err
	}
	return p, duration, supported, nil
}

func (r *ownedMP4Reader) inspect() (p probeInfo, duration int64, supported bool) {
	var moov []byte
	var mdat mp4Span
	ftyp := false
	for at, count := int64(0), 0; at < r.size; count++ {
		if count >= 16 {
			return p, 0, false
		}
		h, ok := r.bytes(at, 8)
		if !ok {
			return p, 0, false
		}
		n := int64(binary.BigEndian.Uint32(h))
		head := int64(8)
		if n == 1 {
			b, ok := r.bytes(at+8, 8)
			if !ok {
				return p, 0, false
			}
			u := binary.BigEndian.Uint64(b)
			if u > math.MaxInt64 {
				return p, 0, false
			}
			n = int64(u)
			head = 16
		}
		if n < head || n > r.size-at {
			return p, 0, false
		}
		switch string(h[4:8]) {
		case "ftyp":
			if ftyp || at != 0 || n != 32 {
				return p, 0, false
			}
			b, ok := r.bytes(at+head, int(n-head))
			if !ok || string(b[:4]) != "isom" {
				return p, 0, false
			}
			ftyp = true
		case "free":
			if n != 8 {
				return p, 0, false
			}
		case "moov":
			if moov != nil || n-head > ownedMP4MaxMoovBytes {
				return p, 0, false
			}
			var ok bool
			moov, ok = r.bytes(at+head, int(n-head))
			if !ok {
				return p, 0, false
			}
		case "mdat":
			if mdat.end != 0 || n == head {
				return p, 0, false
			}
			mdat = mp4Span{at + head, at + n}
		default:
			return p, 0, false
		}
		at += n
	}
	if !ftyp || moov == nil || mdat.end == 0 {
		return p, 0, false
	}
	boxes, ok := mp4Boxes(moov)
	if !ok {
		return p, 0, false
	}
	mvhd, ok := mp4Child(moov, "mvhd")
	if !ok || len(mvhd) != 100 || binary.BigEndian.Uint32(mvhd) != 0 {
		return p, 0, false
	}
	movieScale := int64(binary.BigEndian.Uint32(mvhd[12:]))
	if movieScale < 1 || movieScale > 60000000 {
		return p, 0, false
	}
	movieTicks := int64(binary.BigEndian.Uint32(mvhd[16:]))
	if movieTicks <= 0 || movieTicks > 30*24*3600*movieScale {
		return p, 0, false
	}
	var spans []mp4Span
	var longest int64
	for _, box := range boxes {
		switch box.kind {
		case "mvhd", "udta":
		case "trak":
			if len(p.Streams) >= 2 {
				return p, 0, false
			}
			s, trackDuration, chunks, ok := r.track(box.data, mdat, movieScale)
			if !ok || trackDuration > movieTicks {
				return p, 0, false
			}
			s.Index = len(p.Streams)
			p.Streams = append(p.Streams, s)
			spans = append(spans, chunks...)
			longest = max(longest, trackDuration)
		default:
			return p, 0, false
		}
	}
	if longest != movieTicks || p.selectedVideo() == nil || len(p.Streams) < 1 {
		return p, 0, false
	}
	videoCount, audioCount := 0, 0
	for _, s := range p.Streams {
		if s.CodecType == "video" {
			videoCount++
		}
		if s.CodecType == "audio" {
			audioCount++
		}
	}
	if videoCount != 1 || audioCount > 1 {
		return p, 0, false
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	at := mdat.start
	for _, span := range spans {
		if span.start != at || span.end <= span.start {
			return p, 0, false
		}
		at = span.end
	}
	if at != mdat.end {
		return p, 0, false
	}
	// MOV's format clock is rescaled to AV_TIME_BASE before FFprobe prints
	// six decimal places. Newer muxers can use a common encoder clock for mvhd,
	// so preserve native microsecond and subsequent float/MS rounding.
	micros := (movieTicks*1000000 + movieScale/2) / movieScale
	p.Format.Duration = fmt.Sprintf("%d.%06d", micros/1000000, micros%1000000)
	seconds, err := strconv.ParseFloat(p.Format.Duration, 64)
	if err != nil {
		return p, 0, false
	}
	duration = int64(math.Round(seconds * 1000))
	p.Format.StartTime = "0.000000"
	return p, duration, true
}

func (r *ownedMP4Reader) track(data []byte, mdat mp4Span, movieScale int64) (s probeStream, duration int64, spans []mp4Span, ok bool) {
	tkhd, good := mp4Child(data, "tkhd")
	if !good || len(tkhd) != 84 || binary.BigEndian.Uint32(tkhd) != 3 {
		return
	}
	duration = int64(binary.BigEndian.Uint32(tkhd[20:]))
	if duration <= 0 {
		return
	}
	edts, good := mp4Child(data, "edts")
	if !good {
		return
	}
	elst, good := mp4Child(edts, "elst")
	if !good || len(elst) != 20 || binary.BigEndian.Uint32(elst) != 0 || binary.BigEndian.Uint32(elst[4:]) != 1 || binary.BigEndian.Uint32(elst[16:]) != 0x10000 {
		return
	}
	editDuration := int64(binary.BigEndian.Uint32(elst[8:]))
	mediaStart := int64(int32(binary.BigEndian.Uint32(elst[12:])))
	if mediaStart < 0 {
		return
	}
	mdia, good := mp4Child(data, "mdia")
	if !good {
		return
	}
	mdhd, good := mp4Child(mdia, "mdhd")
	if !good || len(mdhd) != 24 || binary.BigEndian.Uint32(mdhd) != 0 {
		return
	}
	scale := binary.BigEndian.Uint32(mdhd[12:])
	ticks := binary.BigEndian.Uint32(mdhd[16:])
	if scale == 0 || scale > 1000000 || ticks == 0 {
		return
	}
	if mediaStart >= int64(ticks) {
		return
	}
	// The muxer rounds the presentation duration up for tkhd. For elst it
	// rounds the full media duration up and separately rounds the negative
	// AAC priming offset down. Those exact expressions can differ by one
	// movie tick; this is not a duration tolerance or a guessed adjustment.
	ceil := func(t int64) int64 { return (t*movieScale + int64(scale) - 1) / int64(scale) }
	legacyDuration := duration == ceil(int64(ticks)-mediaStart) && editDuration == ceil(int64(ticks))-ceil(mediaStart)
	// New muxers retain full AAC frame durations in mdhd/stts and trim the
	// last frame in the edit. Admit only an integral audio/movie clock ratio and
	// less than one AAC frame of trailing padding; all other shapes fall back.
	fullAACFrames := false
	if movieScale%int64(scale) == 0 {
		ratio := movieScale / int64(scale)
		padding := int64(ticks) - mediaStart - editDuration/ratio
		fullAACFrames = editDuration%ratio == 0 && duration == editDuration && padding >= 0 && padding < 1024
	}
	s.TimeBase = "1/" + strconv.FormatUint(uint64(scale), 10)
	s.StartTime = "0.000000"
	hdlr, good := mp4Child(mdia, "hdlr")
	if !good || len(hdlr) < 24 {
		return
	}
	minf, good := mp4Child(mdia, "minf")
	if !good {
		return
	}
	// All sample data must be self-contained, never an external data reference.
	dinf, good := mp4Child(minf, "dinf")
	if !good {
		return
	}
	dref, good := mp4Child(dinf, "dref")
	if !good || len(dref) < 8 || binary.BigEndian.Uint32(dref) != 0 || binary.BigEndian.Uint32(dref[4:]) != 1 {
		return
	}
	refs, good := mp4Boxes(dref[8:])
	if !good || len(refs) != 1 || refs[0].kind != "url " || len(refs[0].data) != 4 || binary.BigEndian.Uint32(refs[0].data) != 1 {
		return
	}
	stbl, good := mp4Child(minf, "stbl")
	if !good {
		return
	}
	stsd, good := mp4Child(stbl, "stsd")
	if !good || len(stsd) < 8 || binary.BigEndian.Uint32(stsd) != 0 || binary.BigEndian.Uint32(stsd[4:]) != 1 {
		return
	}
	entries, good := mp4Boxes(stsd[8:])
	if !good || len(entries) != 1 {
		return
	}
	entry := entries[0]
	if len(entry.data) < 8 || binary.BigEndian.Uint16(entry.data[6:]) != 1 {
		return
	}
	switch string(hdlr[8:12]) {
	case "vide":
		if mediaStart != 0 || !legacyDuration {
			return
		}
		if entry.kind != "avc1" || len(entry.data) < 78 {
			return
		}
		s.CodecType, s.CodecName, s.PixelFormat = "video", "h264", "yuv420p"
		s.Width = int(binary.BigEndian.Uint16(entry.data[24:]))
		s.Height = int(binary.BigEndian.Uint16(entry.data[26:]))
		s.SampleAspectRatio = "1:1"
		avcc, good := mp4Child(entry.data[78:], "avcC")
		if !good || !ownedMP4AVCC(avcc) {
			return
		}
		if uint32(s.Width)<<16 != binary.BigEndian.Uint32(tkhd[76:]) || uint32(s.Height)<<16 != binary.BigEndian.Uint32(tkhd[80:]) {
			return
		}
	case "soun":
		if mediaStart != 1024 || (!legacyDuration && !fullAACFrames) {
			return
		}
		if entry.kind != "mp4a" || len(entry.data) < 28 || binary.BigEndian.Uint16(entry.data[8:]) != 0 {
			return
		}
		esds, good := mp4Child(entry.data[28:], "esds")
		if !good {
			return
		}
		rate, channels, good := ownedMP4AAC(esds)
		if !good || uint32(rate) != scale {
			return
		}
		s.CodecType, s.CodecName, s.Channels, s.SampleRate = "audio", "aac", channels, strconv.Itoa(rate)
	default:
		return
	}
	// Fast producer has no reordered video timestamps; unknown edit/ctts
	// semantics go through the native inspector instead of being approximated.
	children, good := mp4Boxes(stbl)
	if !good {
		return
	}
	for _, box := range children {
		switch box.kind {
		case "stsd", "stts", "stss", "stsc", "stsz", "stco", "co64", "sgpd", "sbgp":
		default:
			return
		}
	}
	sizes, good := mp4Child(stbl, "stsz")
	if !good || len(sizes) < 12 || binary.BigEndian.Uint32(sizes) != 0 {
		return
	}
	count := binary.BigEndian.Uint32(sizes[8:])
	fixed := binary.BigEndian.Uint32(sizes[4:])
	if count == 0 || count > ownedMP4MaxSamples || (fixed == 0 && len(sizes) != 12+int(count)*4) || (fixed != 0 && len(sizes) != 12) {
		return
	}
	stts, good := mp4Child(stbl, "stts")
	if !good || len(stts) < 8 || binary.BigEndian.Uint32(stts) != 0 {
		return
	}
	n := int(binary.BigEndian.Uint32(stts[4:]))
	if n == 0 || n > ownedMP4MaxSamples || len(stts) != 8+n*8 {
		return
	}
	var ns, nt uint64
	for i := 0; i < n; i++ {
		c := binary.BigEndian.Uint32(stts[8+i*8:])
		d := binary.BigEndian.Uint32(stts[12+i*8:])
		if c == 0 || d == 0 {
			return
		}
		ns += uint64(c)
		nt += uint64(c) * uint64(d)
	}
	if ns != uint64(count) || nt != uint64(ticks) {
		return
	}
	if sync, present := mp4Child(stbl, "stss"); present {
		if len(sync) < 8 || binary.BigEndian.Uint32(sync) != 0 {
			return
		}
		n := int(binary.BigEndian.Uint32(sync[4:]))
		if n < 1 || n > int(count) || len(sync) != 8+4*n {
			return
		}
		var last uint32
		for i := 0; i < n; i++ {
			v := binary.BigEndian.Uint32(sync[8+i*4:])
			if v <= last || v > count {
				return
			}
			last = v
		}
	}
	stsc, good := mp4Child(stbl, "stsc")
	if !good || len(stsc) < 8 || binary.BigEndian.Uint32(stsc) != 0 {
		return
	}
	runs := int(binary.BigEndian.Uint32(stsc[4:]))
	if runs < 1 || runs > int(count) || len(stsc) != 8+12*runs {
		return
	}
	offsets, good := mp4Child(stbl, "stco")
	stride := 4
	if !good {
		offsets, good = mp4Child(stbl, "co64")
		stride = 8
	}
	if !good || len(offsets) < 8 || binary.BigEndian.Uint32(offsets) != 0 {
		return
	}
	chunks := int(binary.BigEndian.Uint32(offsets[4:]))
	if chunks < 1 || chunks > int(count) || len(offsets) != 8+stride*chunks {
		return
	}
	var previous uint32
	for i := 0; i < runs; i++ {
		first := binary.BigEndian.Uint32(stsc[8+i*12:])
		per := binary.BigEndian.Uint32(stsc[12+i*12:])
		desc := binary.BigEndian.Uint32(stsc[16+i*12:])
		if first <= previous || (i == 0 && first != 1) || first > uint32(chunks) || per == 0 || per > count || desc != 1 {
			return
		}
		previous = first
	}
	sample, run := uint32(0), 0
	for chunk := 0; chunk < chunks; chunk++ {
		if r.ctx.Err() != nil {
			return
		}
		if run+1 < runs && uint32(chunk+1) >= binary.BigEndian.Uint32(stsc[8+(run+1)*12:]) {
			run++
		}
		per := binary.BigEndian.Uint32(stsc[12+run*12:])
		if per > count-sample {
			return
		}
		var offset uint64
		if stride == 4 {
			offset = uint64(binary.BigEndian.Uint32(offsets[8+chunk*4:]))
		} else {
			offset = binary.BigEndian.Uint64(offsets[8+chunk*8:])
		}
		if offset > uint64(mdat.end) || offset < uint64(mdat.start) {
			return
		}
		at := int64(offset)
		for j := uint32(0); j < per; j++ {
			size := fixed
			if size == 0 {
				size = binary.BigEndian.Uint32(sizes[12+int(sample)*4:])
			}
			if size == 0 || int64(size) > mdat.end-at {
				return
			}
			if s.CodecType == "video" && !r.videoSample(at, int64(size)) {
				return
			}
			at += int64(size)
			sample++
		}
		spans = append(spans, mp4Span{int64(offset), at})
	}
	if sample != count {
		return
	}
	ok = true
	return
}

func ownedMP4AVCC(b []byte) bool {
	if len(b) < 12 || b[0] != 1 || (b[1] != 66 && b[1] != 77) || b[4] != 255 || b[5] != 225 {
		return false
	}
	n := int(binary.BigEndian.Uint16(b[6:]))
	if n < 8 || n > len(b)-9 || b[8] != 0x67 {
		return false
	}
	at := 8 + n
	if b[at] != 1 || len(b)-at < 5 {
		return false
	}
	n = int(binary.BigEndian.Uint16(b[at+1:]))
	return n >= 2 && n == len(b)-at-3 && b[at+3] == 0x68
}

func mp4Descriptor(b []byte, tag byte) (payload, rest []byte, ok bool) {
	if len(b) < 2 || b[0] != tag {
		return
	}
	n := 0
	for i := 1; i <= 4; i++ {
		if len(b) <= i {
			return
		}
		n = (n << 7) | int(b[i]&127)
		if b[i]&128 == 0 {
			if n > len(b)-i-1 {
				return
			}
			return b[i+1 : i+1+n], b[i+1+n:], true
		}
	}
	return
}

func ownedMP4AAC(b []byte) (rate, channels int, ok bool) {
	if len(b) < 4 || binary.BigEndian.Uint32(b) != 0 {
		return
	}
	es, rest, good := mp4Descriptor(b[4:], 3)
	if !good || len(rest) != 0 || len(es) < 3 || es[2] != 0 {
		return
	}
	config, _, good := mp4Descriptor(es[3:], 4)
	if !good || len(config) < 13 || config[0] != 0x40 || config[1] != 0x15 {
		return
	}
	asc, _, good := mp4Descriptor(config[13:], 5)
	if !good || len(asc) != 5 {
		return
	}
	bits := binary.BigEndian.Uint16(asc)
	index := (bits >> 7) & 15
	channels = int((bits >> 3) & 15)
	rates := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	if bits>>11 != 2 || int(index) >= len(rates) || (channels != 1 && channels != 2) || bits&7 != 0 || asc[2] != 0x56 || asc[3] != 0xe5 || asc[4] != 0 {
		return
	}
	return rates[index], channels, true
}

func (r *ownedMP4Reader) videoSample(at, size int64) bool {
	end := at + size
	hasSlice := false
	for n := 0; at < end; n++ {
		if n >= 16 || end-at < 5 {
			return false
		}
		b, ok := r.bytes(at, 5)
		if !ok {
			return false
		}
		length := int64(binary.BigEndian.Uint32(b))
		kind := b[4] & 31
		if length < 1 || length > end-at-4 || b[4]&128 != 0 {
			return false
		}
		switch kind {
		case 1, 5:
			hasSlice = true
		case 6, 7, 8, 9:
		default:
			return false
		}
		at += 4 + length
	}
	return hasSlice && at == end
}
