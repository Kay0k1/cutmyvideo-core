package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestYouTubeNormalizationRecognizesVideoForms(t *testing.T) {
	const id = "aqz-KE_bpKQ"
	expected := "https://www.youtube.com/watch?v=" + id
	for _, raw := range []string{
		"https://www.youtube.com/watch?v=" + id,
		"youtube.com/watch?v=" + id + "&list=PL123&t=250&si=tracking#t=4m",
		"www.youtube.com/watch?feature=share&v=" + id,
		"http://m.youtube.com/watch?v=" + id,
		"http://youtube.com:80/watch?v=" + id,
		"https://WWW.YOUTUBE.COM:443/watch?v=" + id,
		"https://music.youtube.com/watch?v=" + id,
		"https://youtu.be/" + id + "?si=tracking&t=250",
		" www.youtu.be/" + id + " ",
		"https://www.youtube.com/shorts/" + id + "?feature=share",
		"https://youtube.com/live/" + id + "?si=tracking",
		"https://www.youtube.com/embed/" + id + "?start=250",
		"https://www.youtube-nocookie.com/embed/" + id,
		"https://youtube-nocookie.com/embed/" + id + "/",
		"//www.youtube.com/watch?v=" + id,
		"https://www.youtube.com./watch?v=" + id,
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := normalizeSourceURL(raw)
			if err != nil || u.String() != expected {
				t.Fatalf("got %v, %v", u, err)
			}
			if !isPlatformHost(u.Hostname()) {
				t.Fatal("normalized YouTube did not enter platform adapter")
			}
		})
	}
}

func TestYouTubeNormalizationRejectsMalformedAndSpoofedLinks(t *testing.T) {
	for _, raw := range []string{
		"https://youtube.com/watch?v=short",
		"https://youtu.be/aqz-KE-bpKQQ",
		"https://youtube.com/watch",
		"https://youtube.com/watch?v=aqz-KE-bpKQ&v=aqz-KE-bpKQ",
		"https://youtube.com/playlist?list=PL123",
		"https://youtu.be/aqz-KE-bpKQ/extra",
		"https://youtube.com/shorts/aqz-KE-bpKQ/extra",
		"https://youtube.com/live/",
		"https://youtube-nocookie.com/watch?v=aqz-KE-bpKQ",
		"https://youtube.com/watch?v=aqz%2FKE-bpKQ",
		"https://youtube.com/watch?v=aqz+KE-bpKQ",
		"https://youtube.com/watch?v=aqz-KE-bpKQ&si=%ZZ",
		"https://user:password@youtube.com/watch?v=aqz-KE-bpKQ",
		"https://youtube.com@evil.example/watch?v=aqz-KE-bpKQ",
		"https://youtube.com:8443/watch?v=aqz-KE-bpKQ",
		"http://youtube.com:443/watch?v=aqz-KE-bpKQ",
		"ftp://youtube.com/watch?v=aqz-KE-bpKQ",
		"https://youtube.com.evil.example/watch?v=aqz-KE-bpKQ",
		"https://www.youtube.com.evil.example/watch?v=aqz-KE-bpKQ",
		"https://evil.youtube.com/watch?v=aqz-KE-bpKQ",
		"https://www.youtubе.com/watch?v=aqz-KE-bpKQ",
		"https://notyoutube.com/watch?v=aqz-KE-bpKQ",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := normalizeYouTubeURL(raw); !errors.Is(err, errInvalidYouTubeURL) {
				t.Fatalf("accepted %s: %v", raw, err)
			}
		})
	}
}

func TestSourceNormalizationKeepsGeneralHTTPSPolicy(t *testing.T) {
	const direct = "https://cdn.example.org/interview.mp4?token=public-example"
	u, err := normalizeSourceURL(direct)
	if err != nil || u.String() != direct {
		t.Fatal("changed general HTTPS media source")
	}
	for _, raw := range []string{"http://example.org/interview.mp4", "www.example.org/interview.mp4", "//example.org/interview.mp4", "https://youtube.com.evil.example/watch?v=aqz-KE-bpKQ"} {
		if _, err := normalizeSourceURL(raw); err == nil {
			t.Fatalf("accepted unsafe fallback %s", raw)
		}
	}
	if isPlatformHost("youtube.com.evil.example") || isPlatformHost("notyoutube.com") || isPlatformHost("vimeo.com.evil.example") {
		t.Fatal("platform hostname boundary bypass")
	}
}

func TestMalformedYouTubeRejectedBeforeSourcePreparation(t *testing.T) {
	s := NewServer(Config{}, nil)
	for _, raw := range []string{"youtube.com/watch?v=short", "https://youtube.com.evil.example/watch?v=aqz-KE-bpKQ", "https://youtube.com:8443/watch?v=aqz-KE-bpKQ"} {
		body, _ := json.Marshal(map[string]string{"url": raw})
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/api/v1/sources", bytes.NewReader(body))
		s.addSource(w, r, "owner")
		if w.Code != 400 || !bytes.Contains(w.Body.Bytes(), []byte("invalid_youtube_url")) {
			t.Fatalf("unexpected rejection %d %s", w.Code, w.Body.String())
		}
	}
}
