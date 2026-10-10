package app

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestMetadataPayloadCompressionPreservesPrivateFormatsAndLegacyJSON(t *testing.T) {
	_, small := metadataCacheFixture()
	_, large := benchmarkMetadata()
	for _, tt := range []struct {
		name       string
		info       platformInfo
		compressed bool
	}{{"small", small, false}, {"large", large, true}} {
		t.Run(tt.name, func(t *testing.T) {
			legacy, err := json.Marshal(tt.info)
			if err != nil {
				t.Fatal(err)
			}
			var expected platformInfo
			if err := json.Unmarshal(legacy, &expected); err != nil {
				t.Fatal(err)
			}
			payload, err := encodePlatformMetadata(tt.info)
			if err != nil {
				t.Fatal(err)
			}
			compressed := len(payload) >= 2 && payload[0] == 0x1f && payload[1] == 0x8b
			if compressed != tt.compressed {
				t.Fatal("payload compression did not follow its bounded threshold")
			}
			got, ok := decodePlatformMetadata(payload)
			if !ok || !reflect.DeepEqual(got, expected) {
				t.Fatal("metadata round trip changed private URLs, headers, or formats")
			}
			got, ok = decodePlatformMetadata(legacy)
			if !ok || !reflect.DeepEqual(got, expected) {
				t.Fatal("legacy JSON cache rows stopped working")
			}
			got.Formats[0].Headers["Cookie"] = "changed"
			again, ok := decodePlatformMetadata(payload)
			if !ok || again.Formats[0].Headers["Cookie"] != tt.info.Formats[0].Headers["Cookie"] {
				t.Fatal("cached metadata maps were shared across readers")
			}
		})
	}
}

func TestMetadataPayloadRejectsOversizeCorruptionAndConcatenatedMembers(t *testing.T) {
	_, info := benchmarkMetadata()
	payload, err := encodePlatformMetadata(info)
	if err != nil {
		t.Fatal(err)
	}
	corruptCRC := bytes.Clone(payload)
	corruptCRC[len(corruptCRC)-8] ^= 1
	corruptSize := bytes.Clone(payload)
	binary.LittleEndian.PutUint32(corruptSize[len(corruptSize)-4:], 1)
	oversizedSize := bytes.Clone(payload)
	binary.LittleEndian.PutUint32(oversizedSize[len(oversizedSize)-4:], platformMetadataCacheLimit+1)
	var oversized bytes.Buffer
	writer := gzip.NewWriter(&oversized)
	if _, err = writer.Write([]byte(strings.Repeat("x", platformMetadataCacheLimit+1))); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"empty", nil}, {"json", []byte("invalid")},
		{"truncated-header", []byte{0x1f, 0x8b}}, {"truncated-trailer", payload[:len(payload)-3]},
		{"checksum", corruptCRC}, {"wrong-size", corruptSize}, {"large-size", oversizedSize},
		{"large-inflation", oversized.Bytes()}, {"large-raw", []byte(strings.Repeat("x", platformMetadataCacheLimit+1))},
		{"multiple-members", append(bytes.Clone(payload), payload...)}, {"trailing-data", append(bytes.Clone(payload), 1, 2, 3)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, ok := decodePlatformMetadata(tt.data); ok {
				t.Fatal("corrupt or oversized metadata was accepted")
			}
		})
	}
	info.Title = strings.Repeat("x", platformMetadataCacheLimit+1)
	if _, err = encodePlatformMetadata(info); !errors.Is(err, errMetadataNotCacheable) {
		t.Fatal("compression bypassed the uncompressed payload limit")
	}
}

func TestMetadataPayloadKeepsHighEntropyURLsUncompressed(t *testing.T) {
	_, info := metadataPayloadFixture(true)
	payload, err := encodePlatformMetadata(info)
	if err != nil || len(payload) < 2 || payload[0] != '{' {
		t.Fatal("high-entropy URLs incurred unnecessary decompression work")
	}
	got, ok := decodePlatformMetadata(payload)
	if !ok || got.Formats[0].URL != info.Formats[0].URL || len(got.Formats) != len(info.Formats) {
		t.Fatal("uncompressed high-entropy metadata changed")
	}
}

func TestMetadataPayloadParallelEncodingKeepsRetainedPayloadsIndependent(t *testing.T) {
	const workers = 12
	retained := make(chan []byte, workers)
	var writers sync.WaitGroup
	for worker := range workers {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_, info := benchmarkMetadata()
			info.ID = fmt.Sprintf("session-%d", worker)
			info.Title = fmt.Sprintf("private recording %d", worker)
			payload, err := encodePlatformMetadata(info)
			if err != nil {
				t.Error(err)
				return
			}
			snapshot := bytes.Clone(payload)
			// Keep the original alive while later calls reuse compressors.
			for iteration := range 5 {
				info.Title = fmt.Sprintf("replacement %d/%d", worker, iteration)
				if _, err := encodePlatformMetadata(info); err != nil {
					t.Error(err)
					return
				}
			}
			if !bytes.Equal(payload, snapshot) {
				t.Error("a later encode overwrote a retained payload")
			}
			got, ok := decodePlatformMetadata(payload)
			if !ok || got.ID != fmt.Sprintf("session-%d", worker) || got.Title != fmt.Sprintf("private recording %d", worker) {
				t.Error("concurrent cache fills mixed metadata between sessions")
			}
			retained <- payload
		}()
	}
	writers.Wait()
	close(retained)
	if len(retained) != workers {
		t.Fatal("a concurrent cache fill failed")
	}
	// Exercise a raw JSON cache fill after the compressors have been reused,
	// and verify all earlier compressed outputs remain valid afterward.
	_, unique := metadataPayloadFixture(true)
	if _, err := encodePlatformMetadata(unique); err != nil {
		t.Fatal(err)
	}
	for payload := range retained {
		if _, ok := decodePlatformMetadata(payload); !ok {
			t.Fatal("retained metadata was damaged by a later raw JSON fill")
		}
	}
}

func TestMetadataCacheCompressedRowsPersistAcrossWorkers(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	source, info := benchmarkMetadata()
	source.ID = newID("src")
	if err := s.AddSource(ctx, source); err != nil {
		t.Fatal(err)
	}
	if err := s.CachePlatformMetadata(ctx, source, info); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	if err := s.DB.QueryRow(ctx, "SELECT payload FROM source_metadata_cache WHERE source_id=$1", source.ID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if len(payload) < 2 || payload[0] != 0x1f || payload[1] != 0x8b {
		t.Fatal("large persisted metadata was not compressed")
	}
	worker, err := OpenStore(ctx, s.DB.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer worker.DB.Close()
	got, hit, err := worker.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
		t.Error("compressed metadata was resolved again")
		return platformInfo{}, errors.New("unexpected")
	})
	if err != nil || !hit || len(got.Formats) != len(info.Formats) || got.Formats[0].Headers["Cookie"] != info.Formats[0].Headers["Cookie"] {
		t.Fatal("compressed private metadata was lost across workers")
	}
	foreign := source
	foreign.Owner = "other-owner"
	if _, hit := readPlatformMetadataCache(ctx, worker.DB, foreign); hit {
		t.Fatal("compressed metadata bypassed owner isolation")
	}
}
