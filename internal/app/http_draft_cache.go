package app

import (
	"io"
	"net/http"
	"os"
	"strings"
)

// draftTransferWriter observes both io.Copy paths without hiding the original
// writer from ResponseController or bypassing the source's cancellation reader.
type draftTransferWriter struct {
	http.ResponseWriter
	status  int
	written int64
	failed  bool
}

func (w *draftTransferWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *draftTransferWriter) WriteHeader(status int) {
	if w.status == 0 && status >= 200 {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *draftTransferWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.written += int64(n)
	w.failed = w.failed || err != nil || n != len(p)
	return n, err
}

func (w *draftTransferWriter) ReadFrom(r io.Reader) (int64, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	var n int64
	var err error
	if fast, ok := w.ResponseWriter.(io.ReaderFrom); ok {
		n, err = fast.ReadFrom(r)
	} else {
		// Hide ReadFrom to avoid recursion. Count once, including partial errors.
		n, err = io.Copy(struct{ io.Writer }{w.ResponseWriter}, r)
	}
	w.written += n
	w.failed = w.failed || err != nil
	return n, err
}

func (w *draftTransferWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	w.failed = w.failed || err != nil
	return err
}

// A Flusher without FlushError cannot report a failed buffered tail. The
// response still works normally; only the optional reclaim hint is skipped.
func errorReportingDraftFlush(w http.ResponseWriter) bool {
	for {
		switch value := w.(type) {
		case interface{ FlushError() error }:
			return true
		case http.Flusher:
			// ResponseController also stops here; an underlying error is hidden.
			return false
		case interface{ Unwrap() http.ResponseWriter }:
			w = value.Unwrap()
		default:
			return false
		}
	}
}

func serveDraftArtifactContent(w http.ResponseWriter, r *http.Request, name string, info os.FileInfo, file *os.File, reclaim func(*os.File)) {
	content := contextReadSeeker{r.Context(), file}
	eligible := r.Method == http.MethodGet && w.Header().Get("Content-Encoding") == "" && info.Mode().IsRegular() && info.Size() > 0
	for key := range r.Header {
		if strings.EqualFold(key, "Range") {
			eligible = false
		}
	}
	if !eligible {
		http.ServeContent(w, r, name, info.ModTime(), content)
		return
	}
	transfer := &draftTransferWriter{ResponseWriter: w}
	http.ServeContent(transfer, r, name, info.ModTime(), content)
	if transfer.status != http.StatusOK || transfer.failed || transfer.written != info.Size() || r.Context().Err() != nil || w.Header().Get("Content-Encoding") != "" {
		return
	}
	// Confirm the buffered server write as well. This is not acknowledgement
	// that a client received or durably saved the bytes.
	if !errorReportingDraftFlush(w) || transfer.FlushError() != nil || r.Context().Err() != nil {
		return
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(info, after) || after.Size() != info.Size() || !after.ModTime().Equal(info.ModTime()) || r.Context().Err() != nil {
		return
	}
	// Publication already fsynced the immutable file and directories. Keep this
	// exact descriptor open: a path may have been unlinked/replaced meanwhile.
	reclaim(file)
}
