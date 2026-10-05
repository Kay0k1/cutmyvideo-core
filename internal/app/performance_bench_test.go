package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkHLSParse(b *testing.B) {
	var manifest strings.Builder
	manifest.WriteString("#EXTM3U\n")
	for i := range 10000 {
		fmt.Fprintf(&manifest, "#EXTINF:6,\nsegment-%06d.ts\n", i)
	}
	manifest.WriteString("#EXT-X-ENDLIST\n")
	data := []byte(manifest.String())
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := parseHLS("https://media.example/recording/index.m3u8?token=private", data); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHLSSelect(b *testing.B) {
	p := hlsPlaylist{DurationMS: 600000000, Segments: make([]hlsSegment, 100000)}
	for i := range p.Segments {
		p.Segments[i] = hlsSegment{StartMS: int64(i) * 6000, EndMS: int64(i+1) * 6000}
	}
	r := Range{StartMS: 300000000, EndMS: 300010000}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := selectHLSSegments(p, r); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStorageAdmission(b *testing.B) {
	dir := b.TempDir()
	for i := range 10000 {
		file, err := os.Create(filepath.Join(dir, fmt.Sprintf("source-%05d", i)))
		if err != nil {
			b.Fatal(err)
		}
		if err := file.Close(); err != nil {
			b.Fatal(err)
		}
	}
	c := Config{DataDir: dir, MaxStorageBytes: 1 << 30}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if !storageAvailableContext(context.Background(), c, 0) {
			b.Fatal("empty fixture exceeded storage budget")
		}
	}
}
