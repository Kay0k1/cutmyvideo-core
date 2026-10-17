package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type rateEntry struct {
	count int
	reset time.Time
}

const maxRateEntries = 10000

const requestsPerIPPerMinute = 1200

func (s *Server) validOrigin(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if s.Config.PublicOrigin != "" {
		return origin == strings.TrimSuffix(s.Config.PublicOrigin, "/")
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
}

func ownerFromRequest(r *http.Request) (string, bool) {
	c, err := r.Cookie("cutmy_session")
	if err != nil {
		return "", false
	}
	b, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(b) != 32 {
		return "", false
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), true
}

func (s *Server) allow(key string, limit int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// Bound cleanup work under the shared mutex even when the table is full.
	// Previously every denied request scanned all entries and a full table also
	// rejected existing clients regardless of their own remaining allowance.
	if !now.Before(s.rateCleanup) {
		for k, v := range s.rate {
			if !now.Before(v.reset) {
				delete(s.rate, k)
			}
		}
		s.rateCleanup = now.Add(time.Second)
	}
	v := s.rate[key]
	if v == nil || !now.Before(v.reset) {
		if v == nil && len(s.rate) >= maxRateEntries {
			return false
		}
		v = &rateEntry{reset: now.Add(time.Minute)}
		s.rate[key] = v
	}
	if v.count >= limit {
		return false
	}
	v.count++
	return true
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.allow("session:"+ip, 60) {
		writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
		return
	}
	if _, ok := ownerFromRequest(r); !ok {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			writeError(w, 500, "internal", "Could not create a session")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "cutmy_session", Value: base64.RawURLEncoding.EncodeToString(b), Path: "/", MaxAge: 7 * 24 * 3600, HttpOnly: true, Secure: s.Config.SecureCookie, SameSite: http.SameSiteLaxMode})
	}
	writeJSON(w, 200, map[string]any{"ok": true, "limits": map[string]any{"max_source_bytes": s.Config.MaxSourceBytes, "max_output_bytes": s.Config.MaxOutputBytes, "max_fetch_bytes": remoteSourceBudget(s.Config), "max_ranges": s.Config.MaxRanges, "max_range_ms": s.Config.MaxRangeMS, "max_job_ms": s.Config.MaxJobMS}})
}

type ownerHandler func(http.ResponseWriter, *http.Request, string)

func (s *Server) clientIP(r *http.Request) string {
	if s.Config.TrustProxy {
		parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
		if len(parts) > 0 {
			ip := strings.TrimSpace(parts[len(parts)-1])
			if net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func (s *Server) withSession(next ownerHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		owner, ok := ownerFromRequest(r)
		if !ok {
			writeError(w, 401, "session_required", "Create a session before using this endpoint")
			return
		}
		// Apply IP admission before allocating a per-owner entry: capability
		// cookies are deliberately client-generatable and may be rotated freely.
		ip := s.clientIP(r)
		if !s.allow("requests-ip:"+ip, requestsPerIPPerMinute) {
			writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
			return
		}
		if (r.Method == "POST" || r.Method == "DELETE" || r.Method == "PUT" || r.Method == "PATCH") && !s.allow("mutations:"+ip, s.Config.MutationsPerMinute) {
			writeError(w, 429, "rate_limit", "Too many changes; wait a minute")
			return
		}
		if !s.allow("requests:"+owner, 180) {
			writeError(w, 429, "rate_limit", "Too many requests; wait a minute")
			return
		}
		next(w, r, owner)
	}
}
