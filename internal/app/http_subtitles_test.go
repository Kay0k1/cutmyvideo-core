package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestHTTPSubtitleRetimePublicContract(t *testing.T) {
	python := os.Getenv("CUTMY_CONTRACT_PYTHON")
	if python == "" {
		python = "python3"
	}
	if out, err := exec.Command(python, "-c", "import yaml,jsonschema").CombinedOutput(); err != nil {
		if os.Getenv("CUTMY_REQUIRE_CONTRACT") == "1" {
			t.Fatalf("install scripts/requirements-contract.txt: %v %s", err, out)
		}
		t.Skip("OpenAPI runtime validation needs scripts/requirements-contract.txt")
	}
	server := NewServer(Config{MutationsPerMinute: 100}, nil)
	request := subtitleTestRequest("1\n00:00:01,000 --> 00:00:02,000\nПривет\n", "srt")
	var samples []map[string]any
	for _, test := range []struct {
		name   string
		owner  bool
		format string
		text   string
		status int
	}{
		{"srt", true, "srt", request.Text, 200},
		{"vtt", true, "vtt", "WEBVTT\n\n00:01.000 --> 00:02.000 align:start\nHello\n", 200},
		{"session-required", false, "srt", request.Text, 401},
		{"malformed", true, "srt", "private malformed transcript", 400},
		{"unsupported", true, "vtt", "WEBVTT\n\nSTYLE\n::cue {color:red}\n", 400},
	} {
		request.Format, request.Text, request.OutputFormat = test.format, test.text, test.format
		data, err := json.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/api/v1/subtitles/retime", bytes.NewReader(data))
		if test.owner {
			r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, r)
		if w.Code != test.status {
			t.Fatalf("%s: status %d", test.name, w.Code)
		}
		var body any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		headers := map[string]string{}
		for key := range w.Header() {
			headers[key] = w.Header().Get(key)
		}
		samples = append(samples, map[string]any{"name": test.name, "method": "POST", "route": "/api/v1/subtitles/retime", "request": request, "status": w.Code, "headers": headers, "body": body})
	}
	data, err := json.Marshal(samples)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "subtitle-samples.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(python, filepath.Join("..", "..", "scripts", "check-contract.py"), "--samples", path).CombinedOutput(); err != nil {
		t.Fatalf("subtitle HTTP/OpenAPI contract: %v\n%s", err, output)
	} else {
		t.Log(strings.TrimSpace(string(output)))
	}
}

func TestSubtitleRetimePublicHandler(t *testing.T) {
	server := NewServer(Config{MutationsPerMinute: 100}, nil)
	handler := server.Handler()
	request := subtitleTestRequest("1\n00:00:01,000 --> 00:00:02,000\nHello\n", "srt")
	body, _ := json.Marshal(request)
	call := func(raw []byte, owner bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/subtitles/retime", bytes.NewReader(raw))
		if owner {
			r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := call(body, false); w.Code != 401 {
		t.Fatalf("missing owner: %d %s", w.Code, w.Body.String())
	}
	w := call(body, true)
	var result SubtitleRetimeResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); w.Code != 200 || err != nil || len(result.Outputs) != 1 || result.Outputs[0].CueCount != 1 {
		t.Fatalf("response: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("subtitle text response may be cached")
	}
	if w := call(append(append([]byte{}, body...), []byte("{}")...), true); w.Code != 400 {
		t.Fatal("accepted trailing document")
	}
	if w := call([]byte(`{"version":1,"format":"srt","text":"","outputs":[],"unknown":true}`), true); w.Code != 400 {
		t.Fatal("accepted unknown fields")
	}
	request.Text = "Do not reflect this malformed private transcript"
	bad, _ := json.Marshal(request)
	w = call(bad, true)
	if w.Code != 400 || strings.Contains(w.Body.String(), request.Text) {
		t.Fatalf("unsafe error: %d %s", w.Code, w.Body.String())
	}
}

func TestSubtitleRetimePublicHandlerAllowsBoundedEscapedText(t *testing.T) {
	server := NewServer(Config{MutationsPerMinute: 100}, nil)
	request := subtitleTestRequest("1\n00:00:01,000 --> 00:00:02,000\n"+strings.Repeat("<", 200000)+"\n", "srt")
	body, _ := json.Marshal(request)
	if len(body) <= 1<<20 {
		t.Fatal("test must exceed the ordinary metadata limit")
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/subtitles/retime", bytes.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))})
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("bounded subtitle request refused: %d %s", w.Code, w.Body.String())
	}
}
