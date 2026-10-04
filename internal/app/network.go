package app

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var forbiddenNetworks = func() []netip.Prefix {
	values := [...]string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"}
	prefixes := make([]netip.Prefix, len(values))
	for i, value := range values {
		prefixes[i] = netip.MustParsePrefix(value)
	}
	return prefixes
}()

const networkDialTimeout = 15 * time.Second
const relayBufferSize = 32 << 10

// Only fixed-size arrays enter the pool; upstream sizes never control retained
// memory. Each active response owns its buffer until its copy has completed.
var relayBuffers = sync.Pool{New: func() any { return new([relayBufferSize]byte) }}

func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, prefix := range forbiddenNetworks {
		if prefix.Contains(a) {
			return false
		}
	}
	return true
}

func validateURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return nil, errors.New("use a public HTTPS URL on the standard port")
	}
	if strings.ContainsAny(u.Hostname(), "%\\\x00") {
		return nil, errors.New("invalid host")
	}
	u.Fragment = ""
	return u, nil
}

func safeDial(ctx context.Context, network, address string) (net.Conn, error) {
	d := net.Dialer{Timeout: networkDialTimeout, KeepAlive: 30 * time.Second}
	return dialPublic(ctx, network, address, net.DefaultResolver.LookupIP, d.DialContext)
}

func dialPublic(ctx context.Context, network, address string, lookup func(context.Context, string, string) ([]net.IP, error), dial func(context.Context, string, string) (net.Conn, error)) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	if port != "443" && port != "80" {
		return nil, errors.New("network port is not allowed")
	}
	// This deadline covers DNS and all fallback addresses together. Canceling the
	// dial context after success does not close the established connection.
	ctx, cancel := context.WithTimeout(ctx, networkDialTimeout)
	defer cancel()
	ips, err := lookup(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("host has no addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, errors.New("private or reserved network addresses are not allowed")
		}
	}
	deadline, _ := ctx.Deadline()
	for i, ip := range ips {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attemptDeadline, deadlineErr := partialDialDeadline(time.Now(), deadline, len(ips)-i)
		if deadlineErr != nil {
			return nil, deadlineErr
		}
		attemptContext, attemptCancel := context.WithDeadline(ctx, attemptDeadline)
		conn, dialErr := dial(attemptContext, network, net.JoinHostPort(ip.String(), port))
		attemptCancel()
		if dialErr == nil {
			return conn, nil
		}
		err = dialErr
	}
	return nil, err
}

func partialDialDeadline(now, deadline time.Time, addressesRemaining int) (time.Time, error) {
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return time.Time{}, context.DeadlineExceeded
	}
	if addressesRemaining == 1 {
		return deadline, nil
	}
	timeout := remaining / time.Duration(addressesRemaining)
	// Match net.Dialer's two-second minimum within the normal dial budget.
	// Shorter caller budgets still share time, so one address cannot consume
	// the entire request deadline before an ordered fallback is attempted.
	const minimum = 2 * time.Second
	if timeout < minimum && remaining >= minimum {
		timeout = minimum
	}
	return now.Add(timeout), nil
}

func safeClient() *http.Client {
	t := &http.Transport{Proxy: nil, DialContext: safeDial, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 20 * time.Second, MaxIdleConns: 20, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: t, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		_, err := validateURL(req.URL.String())
		return err
	}}
}

// networkGuard provides an explicit proxy to yt-dlp and an authenticated media
// relay for FFmpeg. Every upstream connection resolves and pins public IPs.
type networkGuard struct {
	server      *http.Server
	listener    net.Listener
	client      *http.Client
	base, token string
	mu          sync.RWMutex
	streams     map[string]relaySource
	bytes       atomic.Int64
	limit       int64
	ctx         context.Context
	cancel      context.CancelFunc
}

func newNetworkGuard(limit int64) (*networkGuard, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	b := make([]byte, 24)
	if _, err = rand.Read(b); err != nil {
		l.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	g := &networkGuard{listener: l, client: safeClient(), base: "http://" + l.Addr().String(), token: hex.EncodeToString(b), streams: map[string]relaySource{}, limit: limit, ctx: ctx, cancel: cancel}
	g.server = &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, BaseContext: func(net.Listener) context.Context { return ctx }}
	go func() { _ = g.server.Serve(l) }()
	return g, nil
}

func (g *networkGuard) Close() {
	g.cancel()
	_ = g.server.Close()
	g.client.CloseIdleConnections()
}
func (g *networkGuard) ProxyURL() string {
	// urllib omits Proxy-Authorization when either credential is empty.
	return "http://" + g.token + ":" + g.token + "@" + g.listener.Addr().String()
}

type relaySource struct {
	URL     string
	Headers map[string]string
}

func (g *networkGuard) Relay(raw string) (string, error) { return g.RelayWithHeaders(raw, nil) }
func (g *networkGuard) RelayWithHeaders(raw string, headers map[string]string) (string, error) {
	if _, err := validateURL(raw); err != nil {
		return "", err
	}
	id := newID("stream")
	g.mu.Lock()
	g.streams[id] = relaySource{URL: raw, Headers: headers}
	g.mu.Unlock()
	return g.base + "/relay/" + g.token + "/" + id, nil
}

func (g *networkGuard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/relay/") && !r.URL.IsAbs() {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) != 4 || parts[2] != g.token {
			http.NotFound(w, r)
			return
		}
		g.mu.RLock()
		source, ok := g.streams[parts[3]]
		g.mu.RUnlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			w.WriteHeader(405)
			return
		}
		u, _ := url.Parse(source.URL)
		g.forward(w, r, u, false, source.Headers)
		return
	}
	user, password, ok := proxyCredentials(r.Header.Get("Proxy-Authorization"))
	if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(g.token)) != 1 || subtle.ConstantTimeCompare([]byte(password), []byte(g.token)) != 1 {
		w.WriteHeader(http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		g.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
		http.Error(w, "unsupported proxy scheme", 400)
		return
	}
	if r.URL.User != nil || (r.URL.Port() != "" && r.URL.Port() != "80" && r.URL.Port() != "443") {
		http.Error(w, "blocked proxy destination", 400)
		return
	}
	g.forward(w, r, r.URL, true, nil)
}

func proxyCredentials(value string) (string, string, bool) {
	r := &http.Request{Header: http.Header{}}
	r.Header.Set("Authorization", value)
	return r.BasicAuth()
}

func (g *networkGuard) forward(w http.ResponseWriter, r *http.Request, u *url.URL, proxy bool, headers map[string]string) {
	if g.bytes.Load() >= g.limit {
		http.Error(w, "transfer limit reached", 413)
		return
	}
	method := r.Method
	if method != "GET" && method != "HEAD" && method != "POST" {
		http.Error(w, "method blocked", 405)
		return
	}
	var body io.Reader
	if proxy && r.Body != nil {
		body = io.LimitReader(r.Body, 1<<20)
	}
	req, err := http.NewRequestWithContext(r.Context(), method, u.String(), body)
	if err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	for _, name := range []string{"Range", "If-Range", "User-Agent", "Accept", "Accept-Encoding", "Content-Type", "Cookie", "Authorization"} {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "cutmy-core/0.1")
	}
	if !proxy {
		req.Header.Del("Cookie")
		req.Header.Del("Authorization")
		mediaHeaders(req, headers)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		http.Error(w, "upstream unavailable or blocked", 502)
		return
	}
	defer resp.Body.Close()
	for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
		if v := resp.Header.Get(name); v != "" {
			w.Header().Set(name, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if method == "HEAD" {
		return
	}
	buffer := relayBuffers.Get().(*[relayBufferSize]byte)
	defer relayBuffers.Put(buffer)
	buf := buffer[:]
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if g.bytes.Add(int64(n)) > g.limit {
				return
			}
			if _, err = w.Write(buf[:n]); err != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

func (g *networkGuard) connect(w http.ResponseWriter, r *http.Request) {
	_, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		http.Error(w, "only HTTPS tunnels are allowed", 403)
		return
	}
	conn, err := safeDial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "destination blocked", 403)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		conn.Close()
		w.WriteHeader(500)
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		conn.Close()
		return
	}
	g.tunnel(r.Context(), client, conn, rw)
}

func (g *networkGuard) tunnel(ctx context.Context, client, conn net.Conn, rw *bufio.ReadWriter) {
	closeConnections := func() { _ = client.Close(); _ = conn.Close() }
	// http.Server.Close does not own hijacked sockets. The server's base context
	// lets guard.Close interrupt both tunnel directions, including blocked reads.
	stop := context.AfterFunc(ctx, closeConnections)
	defer stop()
	defer closeConnections()
	_ = client.SetDeadline(time.Now().Add(60 * time.Second))
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))
	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	if err := rw.Flush(); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { _, _ = io.Copy(conn, rw); _ = conn.Close(); close(done) }()
	buffer := relayBuffers.Get().(*[relayBufferSize]byte)
	defer relayBuffers.Put(buffer)
	buf := buffer[:]
	for {
		n, e := conn.Read(buf)
		if n > 0 {
			if g.bytes.Add(int64(n)) > g.limit {
				break
			}
			if _, err := client.Write(buf[:n]); err != nil {
				break
			}
		}
		if e != nil {
			break
		}
	}
	closeConnections()
	<-done
}
