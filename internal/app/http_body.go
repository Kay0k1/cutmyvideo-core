package app

import (
	"io"
	"net/http"
	"time"
)

type trackedRequestBody struct {
	io.ReadCloser
	complete bool
}

func (b *trackedRequestBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.complete = true
	}
	return n, err
}

type bodyGuardResponseWriter struct {
	http.ResponseWriter
	body        *trackedRequestBody
	http1       bool
	wroteHeader bool
}

func (w *bodyGuardResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *bodyGuardResponseWriter) WriteHeader(status int) {
	if !w.wroteHeader && status >= 200 {
		w.wroteHeader = true
		deadline := time.Time{}
		if !w.body.complete {
			// Refuse reuse before committing the response. Expiring an HTTP/1
			// read can cancel its connection context, including future requests.
			if w.http1 {
				w.Header().Set("Connection", "close")
			}
			deadline = time.Now()
		}
		// Fully consumed bodies must retain a live connection context: an
		// expired deadline would also stop net/http's disconnect reader.
		_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *bodyGuardResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}
