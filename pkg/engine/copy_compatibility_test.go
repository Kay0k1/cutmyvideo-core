package engine

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Preserve the 1.x contract: compatible-copy decisions belong to the configured
// FFmpeg muxer. New muxer capabilities must not be blocked by a static table.
func TestCopyRetainsConfiguredMuxerCodecCompatibility(t *testing.T) {
	for _, tool := range []string{"ffmpeg", "ffprobe"} {
		if _, e := exec.LookPath(tool); e != nil {
			t.Skip(tool + " is not installed")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, tc := range []struct{ videoEncoder, videoCodec, audioEncoder, audioCodec string }{
		{"libx264", "h264", "libopus", "opus"},
		{"libx264", "h264", "libvorbis", "vorbis"},
		{"libx264", "h264", "flac", "flac"},
		{"libx264", "h264", "pcm_s16le", "pcm_s16le"},
		{"mpeg1video", "mpeg1video", "mp2", "mp2"},
		{"mpeg2video", "mpeg2video", "mp2", "mp2"},
	} {
		t.Run(tc.videoCodec+"-"+tc.audioCodec, func(t *testing.T) {
			dir := t.TempDir()
			input := filepath.Join(dir, "source.mkv")
			made, e := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-f", "lavfi", "-i", "testsrc2=duration=2:size=160x96:rate=25", "-f", "lavfi", "-i", "sine=duration=2:sample_rate=48000", "-c:v", tc.videoEncoder, "-threads", "1", "-c:a", tc.audioEncoder, input).CombinedOutput()
			if e != nil {
				if strings.Contains(string(made), "Unknown encoder") || strings.Contains(string(made), "Encoder not found") {
					t.Skip("fixture encoder is not installed")
				}
				t.Fatalf("fixture: %v %s", e, made)
			}
			oracle := filepath.Join(dir, "muxer-copy.mp4")
			oracleData, oracleErr := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", input, "-map", "0:v:0", "-map", "0:a:0", "-c", "copy", oracle).CombinedOutput()
			if oracleErr != nil {
				if strings.Contains(string(oracleData), "not currently supported in container") || strings.Contains(string(oracleData), "experimental") {
					t.Skip("configured muxer rejects this codec pair")
				}
				t.Fatalf("muxer oracle: %v %s", oracleErr, oracleData)
			}
			output := filepath.Join(dir, "engine-copy.mp4")
			if _, e := New(Config{FFmpegThreads: 1}).Export(ctx, input, output, Range{EndMS: 1200}, Options{Format: "mp4", Quality: "best", CutMode: "copy"}); e != nil {
				t.Fatalf("compatible 1.x copy rejected: %v", e)
			}
			oracleRaw, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "json", oracle).Output()
			if e != nil {
				t.Fatal(e)
			}
			var oracleInfo struct {
				Streams []struct {
					Codec string `json:"codec_name"`
				} `json:"streams"`
			}
			if e = json.Unmarshal(oracleRaw, &oracleInfo); e != nil {
				t.Fatal(e)
			}
			raw, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "stream=codec_type,codec_name", "-of", "json", output).Output()
			if e != nil {
				t.Fatal(e)
			}
			var info struct {
				Streams []struct {
					Type  string `json:"codec_type"`
					Codec string `json:"codec_name"`
				} `json:"streams"`
			}
			if e = json.Unmarshal(raw, &info); e != nil {
				t.Fatal(e)
			}
			if len(info.Streams) != 2 || len(oracleInfo.Streams) != 2 || info.Streams[0].Codec != oracleInfo.Streams[0].Codec || info.Streams[1].Codec != oracleInfo.Streams[1].Codec {
				t.Fatalf("copy differs from the configured muxer's codec tags: %s; oracle %s", raw, oracleRaw)
			}
			if tc.audioCodec == "mp2" {
				// MP4's MPEG-audio descriptor may report mp3 for unchanged MP2
				// packets. Verify the payload, rather than mistake that label
				// for an implicit encoder or require one particular tool tag.
				packetHashes := func(path string) []string {
					data, e := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-select_streams", "a:0", "-show_packets", "-show_data_hash", "sha256", "-show_entries", "packet=data_hash", "-of", "json", path).Output()
					if e != nil {
						t.Fatal(e)
					}
					var packets struct {
						Packets []struct {
							Hash string `json:"data_hash"`
						} `json:"packets"`
					}
					if e = json.Unmarshal(data, &packets); e != nil {
						t.Fatal(e)
					}
					var hashes []string
					for _, packet := range packets.Packets {
						hashes = append(hashes, packet.Hash)
					}
					return hashes
				}
				original := make(map[string]bool)
				for _, hash := range packetHashes(input) {
					original[hash] = true
				}
				copied := packetHashes(output)
				if len(copied) == 0 {
					t.Fatal("copied MPEG audio has no packets")
				}
				for _, hash := range copied {
					if !original[hash] {
						t.Fatal("MPEG audio packet was re-encoded")
					}
				}
			}
			if decoded, e := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-xerror", "-i", output, "-f", "null", "-").CombinedOutput(); e != nil {
				t.Fatalf("independent decode: %v %s", e, decoded)
			}
		})
	}
}
