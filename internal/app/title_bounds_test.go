package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSourceTitlesBoundUntrustedExtractorMetadata(t *testing.T) {
	for _, raw := range []string{strings.Repeat("тест", 100000), strings.Repeat("🎬", 100000), "\x00\r\n\t", string([]byte{0xff, 0xfe})} {
		title := normalizeSourceTitle(raw)
		if title == "" || len(title) > 512 || !utf8.ValidString(title) || strings.ContainsAny(title, "\x00\r\n\t") {
			t.Fatalf("invalid retained title: bytes=%d valid=%v", len(title), utf8.ValidString(title))
		}
	}
	if got := normalizeSourceTitle("  Мой фильм 🎬  "); got != "Мой фильм 🎬" {
		t.Fatal("ordinary title changed", got)
	}
}
