package mediapreflight

import (
	"strings"

	"github.com/Kay0k1/cutmyvideo-core/internal/mediainfo"
)

const CapabilityBytes = 1 << 20

// ApplyListing interprets one already-observed capability table. Its caller
// retains query deadlines, tool identity and fatal operating-system errors.
func ApplyListing(group CapabilityGroup, available map[string]bool, deferred bool, result *Result) error {
	if deferred {
		AddDeferred(result, "tool_"+group.Listing, "tool_capabilities_unverified", "The configured media tool's "+group.Listing+" could not be verified without processing media")
	} else {
		for _, name := range group.Required {
			if !available[name] {
				return &mediainfo.Problem{Code: group.Code, Message: "The configured media tool does not provide required " + group.Listing + " capability " + name}
			}
		}
		result.Checks = append(result.Checks, Check{Name: "tool_" + group.Listing, Status: "passed"})
	}
	return nil
}

// Capability names are read only from complete, recognized FFmpeg tables.
// A substring in descriptive text or an unrecognized response proves nothing.
func ParseCapabilityList(kind string, data []byte) (map[string]bool, bool) {
	if len(data) == 0 || len(data) > CapabilityBytes {
		return nil, false
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	header := strings.TrimSpace(lines[0])
	validHeader := kind == "encoders" && header == "Encoders:" || kind == "filters" && header == "Filters:" || kind == "bsfs" && header == "Bitstream filters:" || (kind == "muxers" || kind == "demuxers") && (header == "Formats:" || header == "File formats:" || kind == "muxers" && header == "Muxers:" || kind == "demuxers" && header == "Demuxers:")
	if !validHeader {
		return nil, false
	}
	entries := make(map[string]bool)
	rows := kind == "bsfs"
	var filterLegend uint8
	for _, raw := range lines[1:] {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if !rows {
			if strings.Trim(line, "-") == "" {
				rows = true
				continue
			}
			if kind == "filters" {
				// FFmpeg 5.1 starts rows directly after this complete legend;
				// newer versions also place a dashed separator after it.
				legend := []string{"T.. = Timeline support", ".S. = Slice threading", "..C = Command support", "A = Audio input/output", "V = Video input/output", "N = Dynamic number and/or type of input/output", "| = Source or sink filter"}
				matched := false
				for i, entry := range legend {
					if strings.Join(strings.Fields(line), " ") == entry {
						filterLegend |= 1 << i
						matched = true
						break
					}
				}
				if matched {
					continue
				}
				if filterLegend != 127 {
					return nil, false
				}
				rows = true
			} else {
				fields := strings.Fields(line)
				if len(fields) < 3 || fields[1] != "=" {
					return nil, false
				}
				continue
			}
		}
		fields := strings.Fields(line)
		if kind == "bsfs" {
			if len(fields) != 1 || !CapabilityName(fields[0]) {
				return nil, false
			}
			entries[fields[0]] = true
			continue
		}
		// Muxer/demuxer descriptions are optional. FFmpeg 5.1's libcdio
		// demuxer, for example, has only its flags and exact name.
		minimumFields := 3
		if kind == "muxers" || kind == "demuxers" {
			minimumFields = 2
		}
		if len(fields) < minimumFields {
			return nil, false
		}
		flags := fields[0]
		switch kind {
		case "encoders":
			if len(flags) != 6 || !strings.ContainsRune("VAS", rune(flags[0])) || strings.Trim(flags[1:], ".FSXBD") != "" {
				return nil, false
			}
		case "filters":
			if len(flags) < 2 || len(flags) > 3 || strings.Trim(flags, ".TSC") != "" || len(fields) < 4 || !strings.Contains(fields[2], "->") {
				return nil, false
			}
		case "muxers", "demuxers":
			if len(flags) > 3 || strings.Trim(flags, "DEd.") != "" || !strings.ContainsAny(flags, "DE") {
				return nil, false
			}
		}
		for _, name := range strings.Split(fields[1], ",") {
			if !CapabilityName(name) {
				return nil, false
			}
			if kind == "muxers" && !strings.Contains(flags, "E") || kind == "demuxers" && !strings.Contains(flags, "D") {
				continue
			}
			entries[name] = true
		}
	}
	return entries, rows && len(entries) > 0
}

func CapabilityName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, ch := range name {
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '_' || ch == '-' {
			continue
		}
		return false
	}
	return true
}
