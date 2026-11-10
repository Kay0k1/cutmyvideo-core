package mediaqc

import (
	"encoding/binary"
	"errors"
	"io"
)

// maxTopLevelBoxes bounds the scan of a hostile or unusual file layout.
const maxTopLevelBoxes = 4096

var errBoxLayout = errors.New("invalid MP4/MOV top-level box layout")

// FastStart reports whether an MP4/MOV file stores its moov box before the
// first mdat box, so playback can start before the whole file is read. Only
// top-level box headers are read. A file with moov and no mdat (fragmented
// MP4 with media in later fragments) is fast-start. A file without moov, or
// with box sizes outside the file, is an error.
func FastStart(r io.ReaderAt, size int64) (bool, error) {
	var header [16]byte
	moov, mdat := int64(-1), int64(-1)
	offset := int64(0)
	for boxes := 0; offset < size; boxes++ {
		if boxes >= maxTopLevelBoxes || size-offset < 8 {
			return false, errBoxLayout
		}
		if _, err := r.ReadAt(header[:8], offset); err != nil {
			return false, err
		}
		length, headerLength := int64(binary.BigEndian.Uint32(header[:4])), int64(8)
		switch length {
		case 0:
			length = size - offset
		case 1:
			if size-offset < 16 {
				return false, errBoxLayout
			}
			if _, err := r.ReadAt(header[8:16], offset+8); err != nil {
				return false, err
			}
			large := binary.BigEndian.Uint64(header[8:16])
			if large > uint64(size-offset) {
				return false, errBoxLayout
			}
			length, headerLength = int64(large), 16
		}
		if length < headerLength || length > size-offset {
			return false, errBoxLayout
		}
		switch string(header[4:8]) {
		case "moov":
			if moov < 0 {
				moov = offset
			}
		case "mdat":
			if mdat < 0 {
				mdat = offset
			}
		}
		if moov >= 0 && mdat >= 0 {
			break
		}
		offset += length
	}
	if moov < 0 {
		return false, errBoxLayout
	}
	return mdat < 0 || moov < mdat, nil
}
