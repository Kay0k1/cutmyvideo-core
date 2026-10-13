package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Capabilities describe an executable, never a source or an upstream address.
// Bound cross-operation retention and check executable identity on every use;
// cancellation and executable replacement cannot publish an old capability.
var packetDropCapabilities struct {
	sync.Mutex
	entries []packetDropCapability
	clock   uint64
}

type packetDropCapability struct {
	path      string
	file      os.FileInfo
	available bool
	used      uint64
}

func packetDropHelpAvailable(data []byte) bool {
	amount, drop := false, false
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "<string>" {
			continue
		}
		switch strings.TrimPrefix(fields[0], "-") {
		case "amount":
			amount = true
		case "drop":
			drop = true
		}
	}
	return amount && drop
}

func supportsAudioPacketDrop(ctx context.Context, c Config) (bool, error) {
	return supportsAudioPacketDropWithProbe(ctx, c, runCommand)
}

func supportsAudioPacketDropWithProbe(ctx context.Context, c Config, query func(context.Context, string, ...string) ([]byte, error)) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := exec.LookPath(c.FFmpeg)
	if err != nil {
		return false, nil
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return false, nil
	}
	file, err := os.Stat(path)
	if err != nil || !file.Mode().IsRegular() {
		return false, nil
	}
	packetDropCapabilities.Lock()
	for i := range packetDropCapabilities.entries {
		entry := &packetDropCapabilities.entries[i]
		if entry.path == path && os.SameFile(entry.file, file) && entry.file.Size() == file.Size() && entry.file.ModTime().Equal(file.ModTime()) {
			packetDropCapabilities.clock++
			entry.used = packetDropCapabilities.clock
			available := entry.available
			packetDropCapabilities.Unlock()
			return available, nil
		}
	}
	packetDropCapabilities.Unlock()
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	data, queryErr := query(queryCtx, c.FFmpeg, "-hide_banner", "-h", "bsf=noise")
	if err := ctx.Err(); err != nil {
		return false, err
	}
	available := queryErr == nil && packetDropHelpAvailable(data)
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(file, after) || file.Size() != after.Size() || !file.ModTime().Equal(after.ModTime()) {
		return false, nil
	}
	// A local query timeout is an optional-probe failure; do not retain that
	// transient condition across future operations. Legacy tools that explicitly
	// refuse help or lack drop expressions can safely retain a negative result.
	if queryCtx.Err() != nil {
		return false, nil
	}
	packetDropCapabilities.Lock()
	defer packetDropCapabilities.Unlock()
	packetDropCapabilities.clock++
	entry := packetDropCapability{path: path, file: after, available: available, used: packetDropCapabilities.clock}
	for i, cached := range packetDropCapabilities.entries {
		if cached.path == path {
			packetDropCapabilities.entries[i] = entry
			return available, nil
		}
	}
	if len(packetDropCapabilities.entries) < 32 {
		packetDropCapabilities.entries = append(packetDropCapabilities.entries, entry)
	} else {
		oldest := 0
		for i := 1; i < len(packetDropCapabilities.entries); i++ {
			if packetDropCapabilities.entries[i].used < packetDropCapabilities.entries[oldest].used {
				oldest = i
			}
		}
		packetDropCapabilities.entries[oldest] = entry
	}
	return available, nil
}
