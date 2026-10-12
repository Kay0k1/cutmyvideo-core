package app

import (
	"encoding/binary"
	"io"
	"os"
)

// A standard MP4 free box is ignored by media decoders. Its private payload
// stores freshly inspected ORIGINAL upright display geometry, not the rounded
// 480p rendition dimensions. It is charged and published with the same MP4;
// cache hits read only twenty bytes without running a probe or sidecar lookup.
const previewGeometryBytes = 20

func appendPreviewGeometry(path string, width, height int) error {
	if width < 2 || height < 2 || width > maxMediaPixels/height {
		return errUnsupportedStream
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return err
	}
	if stat.Size() >= previewOutputBytes-previewGeometryBytes {
		return errOutputLimit
	}
	var box [previewGeometryBytes]byte
	binary.BigEndian.PutUint32(box[:4], previewGeometryBytes)
	copy(box[4:12], "freeCMYG")
	binary.BigEndian.PutUint32(box[12:16], uint32(width))
	binary.BigEndian.PutUint32(box[16:20], uint32(height))
	n, err := file.Write(box[:])
	if err == nil && n != len(box) {
		err = io.ErrShortWrite
	}
	return err
}

func readPreviewGeometry(file io.ReaderAt, size int64) (width, height int) {
	if size < previewGeometryBytes {
		return
	}
	var box [previewGeometryBytes]byte
	if _, err := file.ReadAt(box[:], size-previewGeometryBytes); err != nil ||
		binary.BigEndian.Uint32(box[:4]) != previewGeometryBytes || string(box[4:12]) != "freeCMYG" {
		return
	}
	w, h := uint64(binary.BigEndian.Uint32(box[12:16])), uint64(binary.BigEndian.Uint32(box[16:20]))
	if w < 2 || h < 2 || w > uint64(maxMediaPixels)/h {
		return
	}
	return int(w), int(h)
}
