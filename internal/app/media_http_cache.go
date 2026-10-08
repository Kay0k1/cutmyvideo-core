package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
)

// These headers are applied only after session ownership and file existence
// have been checked. Revalidation runs those checks on every use, so deletion,
// expiry and cookie changes cannot turn an old preview into a successful hit.
func privateSourceFileHeaders(w http.ResponseWriter, identity string, info os.FileInfo) {
	version := sha256.Sum256(fmt.Appendf(nil, "%s\x00%d\x00%d", identity, info.Size(), info.ModTime().UnixNano()))
	w.Header().Set("Cache-Control", "private, max-age=0, must-revalidate")
	w.Header().Add("Vary", "Cookie")
	w.Header().Set("ETag", fmt.Sprintf("\"%x\"", version))
}
