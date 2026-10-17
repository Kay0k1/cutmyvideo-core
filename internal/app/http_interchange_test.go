package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Kay0k1/cutmyvideo-core/internal/edit"
)

func newInterfacesHTTPRequest(handler http.Handler, ctx context.Context, cookie *http.Cookie, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "https://example.com"+path, strings.NewReader(body)).WithContext(ctx)
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestNewInterfacesHTTPAuthenticationAndStrictObjects(t *testing.T) {
	s := NewServer(Config{PublicOrigin: "https://example.com", MutationsPerMinute: 200}, nil)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	for _, path := range []string{"/api/v1/cut-lists/import", "/api/v1/cut-lists/export", "/api/v1/sources/private/analysis/silence", "/api/v1/sources/private/analysis/video", "/api/v1/sources/private/analysis/loudness"} {
		assertHTTPPreflightError(t, newInterfacesHTTPRequest(s.Handler(), context.Background(), nil, path, `{}`), 401, "session_required")
		for _, body := range []string{`null`, `{"input":"/private/secret"}`, `{} {}`} {
			response := newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, path, body)
			if response.Code != 400 {
				t.Fatalf("%s body=%s got %d %s", path, body, response.Code, response.Body.String())
			}
		}
		r := httptest.NewRequest(http.MethodPost, "https://example.com"+path, strings.NewReader(`{}`))
		r.Header.Set("Origin", "https://attacker.invalid")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		assertHTTPPreflightError(t, w, 403, "origin_rejected")
	}
}

func TestNewInterfacesHTTPCutListMarkersAndOutputClockRoundTrip(t *testing.T) {
	s := NewServer(Config{PublicOrigin: "https://example.com", MutationsPerMinute: 200}, nil)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(make([]byte, 32))}
	imported := newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/import", `{"format":"csv","text":"0,,Привет\n1,,Second\n"}`)
	var list edit.CutList
	if err := json.Unmarshal(imported.Body.Bytes(), &list); err != nil || imported.Code != 200 || len(list.Entries) != 2 || list.Entries[0].EndMS != nil || list.Entries[1].EndMS != nil || list.Entries[0].Label != "Привет" {
		t.Fatal(imported.Code, imported.Body.String(), err)
	}
	if imported.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("conversion was publicly cacheable")
	}
	request, _ := json.Marshal(CutListExportRequest{Format: "csv", CutList: &list})
	exported := newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/export", string(request))
	var text CutListText
	if err := json.Unmarshal(exported.Body.Bytes(), &text); err != nil || exported.Code != 200 || text.Version != 1 || text.Text != "0,,Привет\n1,,Second\n" {
		t.Fatal(exported.Code, exported.Body.String(), err)
	}
	request, _ = json.Marshal(CutListExportRequest{Format: "ffmetadata", CutList: &list})
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/export", string(request)), 400, "invalid_cut_list")
	output := newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/export", `{"format":"ffmetadata","segments":[{"index":0,"label":"actual","source_start_ms":9000,"source_end_ms":10000,"output_start_ms":0,"output_end_ms":1100}]}`)
	if err := json.Unmarshal(output.Body.Bytes(), &text); err != nil || output.Code != 200 || !strings.Contains(text.Text, "START=0\nEND=1100") || strings.Contains(text.Text, "START=9000") {
		t.Fatal(output.Code, output.Body.String(), err)
	}
	for _, body := range []string{`{"format":"csv","cut_list":{"version":1,"entries":[]},"segments":[]}`, `{"format":"csv","segments":[]}`, `{"format":"ffmetadata","cut_list":{"version":1,"entries":[{"start_ms":0,"end_ms":1,"label":"ok","input":"private"}]}}`} {
		response := newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/export", body)
		if response.Code != 400 {
			t.Fatal("ambiguous/invalid conversion accepted", response.Code, response.Body.String())
		}
	}
	identity := httptest.NewRequest(http.MethodGet, "/", nil)
	identity.AddCookie(cookie)
	owner, _ := ownerFromRequest(identity)
	for i := 0; i < 60; i++ {
		s.allow("cutlist:"+owner, 60)
	}
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/import", `{"format":"csv","text":"0,1,x\n"}`), 429, "rate_limit")
}

func TestNewInterfacesHTTPCutListBoundedText(t *testing.T) {
	s := NewServer(Config{PublicOrigin: "https://example.com", MutationsPerMinute: 200}, nil)
	cookie := &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))}
	request, _ := json.Marshal(CutListImportRequest{Format: "csv", Text: strings.Repeat("x", (1<<20)+1)})
	assertHTTPPreflightError(t, newInterfacesHTTPRequest(s.Handler(), context.Background(), cookie, "/api/v1/cut-lists/import", string(request)), 400, "invalid_cut_list")
}
