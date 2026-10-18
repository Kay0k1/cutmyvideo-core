package edit

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxCutListBytes = 1 << 20
const MaxCutListEntries = 128
const maxCutListTimestamp = int64(1<<53 - 1)

// CutListEntry retains markers as a nil EndMS. All timestamps use the original
// source timeline, unless the list was explicitly derived from output segments.
type CutListEntry struct {
	StartMS int64  `json:"start_ms"`
	EndMS   *int64 `json:"end_ms"`
	Label   string `json:"label"`
}

type CutList struct {
	Version int            `json:"version"`
	Entries []CutListEntry `json:"entries"`
}

// ParseCutList imports LosslessCut-style three-column CSV or FFmpeg chapter
// metadata. Decimal seconds and rational chapter clocks round to the nearest
// millisecond, with half milliseconds rounded up. No tools or files are opened.
func ParseCutList(format string, data []byte) (CutList, error) {
	if len(data) == 0 || len(data) > MaxCutListBytes || !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return CutList{}, errors.New("cut list must be nonempty UTF-8 without NUL and at most 1 MiB")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var list CutList
	var err error
	switch format {
	case "csv":
		list, err = parseCutListCSV(data)
	case "ffmetadata":
		list, err = parseCutListChapters(data)
	default:
		return list, errors.New("cut list format must be csv or ffmetadata")
	}
	if err == nil {
		err = ValidateCutList(list)
	}
	return list, err
}

func ValidateCutList(list CutList) error {
	if list.Version != 1 || len(list.Entries) == 0 || len(list.Entries) > MaxCutListEntries {
		return errors.New("cut list version must be 1 and entries must contain 1 through 128 items")
	}
	for i, entry := range list.Entries {
		if entry.StartMS < 0 || entry.StartMS > maxCutListTimestamp || (entry.EndMS != nil && (*entry.EndMS <= entry.StartMS || *entry.EndMS > maxCutListTimestamp)) {
			return fmt.Errorf("entry %d: require a nonnegative start_ms and a later end_ms, within safe integer bounds", i+1)
		}
		if len(entry.Label) > 200 || !utf8.ValidString(entry.Label) || strings.IndexByte(entry.Label, 0) >= 0 {
			return fmt.Errorf("entry %d: label must be valid UTF-8 without NUL and at most 200 bytes", i+1)
		}
	}
	return nil
}

// CutListRanges expands a marker to the next entry's start, or to durationMS
// for the final marker. A missing or ambiguous end is an error, never a dropped
// marker. The original list, its labels and pointer values are not mutated.
func CutListRanges(list CutList, durationMS int64) ([]Range, error) {
	if err := ValidateCutList(list); err != nil {
		return nil, err
	}
	if durationMS < 0 || durationMS > maxCutListTimestamp {
		return nil, errors.New("duration_ms must be a nonnegative safe integer")
	}
	ranges := make([]Range, 0, len(list.Entries))
	for i, entry := range list.Entries {
		end := durationMS
		if entry.EndMS != nil {
			end = *entry.EndMS
		} else if i+1 < len(list.Entries) {
			end = list.Entries[i+1].StartMS
		}
		if end <= entry.StartMS {
			return nil, fmt.Errorf("entry %d: a marker needs a later next entry or explicit duration_ms", i+1)
		}
		if durationMS > 0 && end > durationMS {
			return nil, fmt.Errorf("entry %d: end exceeds duration_ms", i+1)
		}
		ranges = append(ranges, Range{StartMS: entry.StartMS, EndMS: end, Label: entry.Label})
	}
	return ranges, nil
}

// CutListFromSegments exports chapter boundaries on the actual output clock,
// rather than guessing from requested copy-mode bounds or source ordering.
func CutListFromSegments(segments []Segment) (CutList, error) {
	list := CutList{Version: 1, Entries: make([]CutListEntry, 0, len(segments))}
	for _, segment := range segments {
		end := segment.OutputEndMS
		list.Entries = append(list.Entries, CutListEntry{StartMS: segment.OutputStartMS, EndMS: &end, Label: segment.Label})
	}
	return list, ValidateCutList(list)
}

func EncodeCutList(format string, list CutList) ([]byte, error) {
	if err := ValidateCutList(list); err != nil {
		return nil, err
	}
	var output bytes.Buffer
	switch format {
	case "csv":
		writer := csv.NewWriter(&output)
		for _, entry := range list.Entries {
			end := ""
			if entry.EndMS != nil {
				end = cutListSeconds(*entry.EndMS)
			}
			if err := writer.Write([]string{cutListSeconds(entry.StartMS), end, entry.Label}); err != nil {
				return nil, err
			}
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, err
		}
	case "ffmetadata":
		output.WriteString(";FFMETADATA1\n")
		var previousEnd int64
		for i, entry := range list.Entries {
			if entry.EndMS == nil || (i > 0 && entry.StartMS < previousEnd) {
				return nil, errors.New("chapters require explicit ends and chronological nonoverlapping ranges; use CSV for markers or arbitrary edit order")
			}
			fmt.Fprintf(&output, "[CHAPTER]\nTIMEBASE=1/1000\nSTART=%d\nEND=%d\ntitle=%s\n", entry.StartMS, *entry.EndMS, escapeChapterTitle(entry.Label))
			previousEnd = *entry.EndMS
		}
	default:
		return nil, errors.New("cut list format must be csv or ffmetadata")
	}
	if output.Len() > MaxCutListBytes {
		return nil, errors.New("encoded cut list exceeds 1 MiB")
	}
	return output.Bytes(), nil
}

func cutListSeconds(ms int64) string {
	if ms%1000 == 0 {
		return strconv.FormatInt(ms/1000, 10)
	}
	return fmt.Sprintf("%d.%03d", ms/1000, ms%1000)
}

func parseCutListCSV(data []byte) (CutList, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = 3
	list := CutList{Version: 1, Entries: []CutListEntry{}}
	for {
		fields, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return list, fmt.Errorf("invalid three-column cut list CSV: %w", err)
		}
		if len(list.Entries) >= MaxCutListEntries {
			return list, errors.New("cut list exceeds 128 entries")
		}
		start := int64(0)
		if fields[0] != "" {
			start, err = decimalSecondsMS(fields[0])
			if err != nil {
				return list, fmt.Errorf("entry %d start: %w", len(list.Entries)+1, err)
			}
		}
		entry := CutListEntry{StartMS: start, Label: fields[2]}
		if fields[1] != "" {
			end, err := decimalSecondsMS(fields[1])
			if err != nil {
				return list, fmt.Errorf("entry %d end: %w", len(list.Entries)+1, err)
			}
			entry.EndMS = &end
		}
		list.Entries = append(list.Entries, entry)
	}
	return list, nil
}

func decimalSecondsMS(text string) (int64, error) {
	if len(text) == 0 || len(text) > 32 || strings.TrimSpace(text) != text {
		return 0, errors.New("timestamp must be nonnegative decimal seconds")
	}
	dot, digits := false, 0
	for _, r := range text {
		if r == '.' && !dot && digits > 0 {
			dot, digits = true, 0
			continue
		}
		if r < '0' || r > '9' {
			return 0, errors.New("timestamp must be nonnegative decimal seconds without exponents")
		}
		digits++
	}
	if digits == 0 || (dot && digits > 9) {
		return 0, errors.New("timestamp requires 1 through 9 fractional digits")
	}
	rational, ok := new(big.Rat).SetString(text)
	if !ok {
		return 0, errors.New("invalid decimal timestamp")
	}
	return roundedMilliseconds(rational)
}

func roundedMilliseconds(value *big.Rat) (int64, error) {
	if value.Sign() < 0 {
		return 0, errors.New("negative timestamp")
	}
	value = new(big.Rat).Mul(value, big.NewRat(1000, 1))
	whole, remainder := new(big.Int), new(big.Int)
	whole.QuoRem(value.Num(), value.Denom(), remainder)
	if new(big.Int).Lsh(remainder, 1).Cmp(value.Denom()) >= 0 {
		whole.Add(whole, big.NewInt(1))
	}
	if !whole.IsInt64() || whole.Int64() > maxCutListTimestamp {
		return 0, errors.New("timestamp exceeds safe integer millisecond bounds")
	}
	return whole.Int64(), nil
}

func parseCutListChapters(data []byte) (CutList, error) {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasSuffix(line, "\r") && !chapterLineContinues(strings.TrimSuffix(line, "\r")) {
			lines[i] = strings.TrimSuffix(line, "\r")
		}
	}
	if len(lines) == 0 || lines[0] != ";FFMETADATA1" {
		return CutList{}, errors.New("chapter metadata requires ;FFMETADATA1 header")
	}
	list := CutList{Version: 1, Entries: []CutListEntry{}}
	var chapter map[string]string
	finish := func() error {
		if chapter == nil {
			return nil
		}
		if len(list.Entries) >= MaxCutListEntries {
			return errors.New("chapter list exceeds 128 entries")
		}
		base := strings.Split(chapter["TIMEBASE"], "/")
		if len(base) != 2 {
			return errors.New("each chapter requires an explicit positive TIMEBASE fraction")
		}
		numerator, err1 := chapterInteger(base[0])
		denominator, err2 := chapterInteger(base[1])
		if err1 != nil || err2 != nil || numerator <= 0 || denominator <= 0 || numerator > 1e9 || denominator > 1e9 {
			return errors.New("chapter TIMEBASE numerator and denominator must be 1 through 1000000000")
		}
		parse := func(key string) (int64, error) {
			value, err := chapterInteger(chapter[key])
			if err != nil {
				return 0, fmt.Errorf("chapter %s requires a nonnegative integer", key)
			}
			rational := new(big.Rat).Mul(new(big.Rat).SetInt64(value), big.NewRat(numerator, denominator))
			return roundedMilliseconds(rational)
		}
		start, err := parse("START")
		if err != nil {
			return err
		}
		end, err := parse("END")
		if err != nil {
			return err
		}
		list.Entries = append(list.Entries, CutListEntry{StartMS: start, EndMS: &end, Label: chapter["title"]})
		return nil
	}
	for i := 1; i < len(lines); i++ {
		line := lines[i]
		for chapterLineContinues(line) {
			if i+1 >= len(lines) {
				return list, errors.New("unterminated chapter escape")
			}
			i++
			line += "\n" + lines[i]
			if len(line) > 8192 {
				return list, errors.New("chapter metadata line exceeds 8192 bytes")
			}
		}
		if len(line) > 8192 {
			return list, errors.New("chapter metadata line exceeds 8192 bytes")
		}
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		if line == "[CHAPTER]" {
			if err := finish(); err != nil {
				return list, err
			}
			chapter = make(map[string]string)
			continue
		}
		if line[0] == '[' {
			return list, errors.New("only CHAPTER metadata sections are supported")
		}
		key, value, found := strings.Cut(line, "=")
		if !found || key == "" || strings.ContainsAny(key, "\\\r\n") {
			return list, errors.New("chapter metadata requires key=value lines")
		}
		value, err := unescapeChapterTitle(value)
		if err != nil {
			return list, err
		}
		if chapter != nil && (key == "TIMEBASE" || key == "START" || key == "END" || key == "title") {
			if _, duplicate := chapter[key]; duplicate {
				return list, errors.New("duplicate chapter field: " + key)
			}
			chapter[key] = value
		}
	}
	if err := finish(); err != nil {
		return list, err
	}
	return list, nil
}

func chapterInteger(text string) (int64, error) {
	if len(text) == 0 || len(text) > 19 {
		return 0, errors.New("integer is missing or too large")
	}
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, errors.New("nonnegative integer required")
		}
	}
	return strconv.ParseInt(text, 10, 64)
}

func chapterLineContinues(line string) bool {
	count := 0
	for i := len(line) - 1; i >= 0 && line[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

func escapeChapterTitle(title string) string {
	return strings.NewReplacer("\\", "\\\\", "=", "\\=", ";", "\\;", "#", "\\#", "\n", "\\\n", "\r", "\\\r").Replace(title)
}

func unescapeChapterTitle(text string) (string, error) {
	var output strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			if i == len(text) {
				return "", errors.New("unterminated chapter escape")
			}
		} else if text[i] == '#' || text[i] == ';' {
			return "", errors.New("chapter values must escape # and ;")
		}
		output.WriteByte(text[i])
	}
	return output.String(), nil
}
