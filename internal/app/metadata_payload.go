package app

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"io"
)

const platformMetadataCompressionThreshold = 4 << 10

// Short-lived metadata contains repeated signed URLs and request headers.
// Compress only substantial payloads, keeping legacy JSON readable across a
// rolling release. The limit applies before compression as well as on reads.
func encodePlatformMetadata(info platformInfo) ([]byte, error) {
	payload, err := json.Marshal(info)
	if err != nil || len(payload) > platformMetadataCacheLimit {
		return nil, errMetadataNotCacheable
	}
	if len(payload) < platformMetadataCompressionThreshold {
		return payload, nil
	}
	var compressed bytes.Buffer
	writer, err := gzip.NewWriterLevel(&compressed, gzip.BestSpeed)
	if err != nil {
		return nil, errMetadataNotCacheable
	}
	if _, err = writer.Write(payload); err != nil {
		return nil, errMetadataNotCacheable
	}
	if err = writer.Close(); err != nil {
		return nil, errMetadataNotCacheable
	}
	// High-entropy per-format tokens cost more CPU to inflate than they save
	// locally. Persist compression only when it eliminates at least 75% of the
	// transferred bytes; other payloads retain the original JSON read cost.
	if compressed.Len() > len(payload)/4 {
		return payload, nil
	}
	return compressed.Bytes(), nil
}

func decodePlatformMetadata(payload []byte) (platformInfo, bool) {
	var info platformInfo
	if len(payload) == 0 || len(payload) > platformMetadataCacheLimit {
		return info, false
	}
	if len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b {
		if len(payload) < 18 {
			return info, false
		}
		// Our single gzip member carries its uncompressed length in the trailer.
		// Bound that length before allocation, and verify it against the complete
		// checksummed stream. No concatenated members or trailing data are accepted.
		length := binary.LittleEndian.Uint32(payload[len(payload)-4:])
		if length == 0 || length > platformMetadataCacheLimit {
			return info, false
		}
		size := int(length)
		compressed := bytes.NewReader(payload)
		reader, err := gzip.NewReader(compressed)
		if err != nil {
			return info, false
		}
		defer reader.Close()
		reader.Multistream(false)
		decoded := make([]byte, size+1)
		n, err := io.ReadFull(reader, decoded)
		if err != io.ErrUnexpectedEOF || n != size || compressed.Len() != 0 {
			return info, false
		}
		payload = decoded[:n]
	}
	if json.Unmarshal(payload, &info) != nil {
		return platformInfo{}, false
	}
	return info, true
}
