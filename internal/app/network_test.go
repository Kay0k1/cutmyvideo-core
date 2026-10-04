package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type networkTestTransport func(*http.Request) (*http.Response, error)

func (f networkTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPublicIPRetainsEveryForbiddenPrefix(t *testing.T) {
	for _, value := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "fc00::/7", "fe80::/10", "ff00::/8", "2001:db8::/32", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		prefix := netip.MustParsePrefix(value)
		first := prefix.Masked().Addr()
		lastBytes := first.As16()
		bits := prefix.Bits()
		if first.Is4() {
			bits += 96
		}
		for bit := bits; bit < 128; bit++ {
			lastBytes[bit/8] |= 1 << (7 - bit%8)
		}
		last := netip.AddrFrom16(lastBytes)
		for _, addr := range []netip.Addr{first, last} {
			if publicIP(net.IP(addr.AsSlice())) {
				t.Errorf("allowed boundary of forbidden prefix %s", prefix)
			}
		}
	}
	if publicIP(nil) || publicIP(net.IP{1, 2, 3}) {
		t.Fatal("allowed invalid IP")
	}
	if !publicIP(net.ParseIP("::ffff:8.8.8.8")) {
		t.Fatal("IPv4-mapped public address rejected")
	}
}

type networkFailingWriter struct{ header http.Header }

func (w *networkFailingWriter) Header() http.Header { return w.header }
func (*networkFailingWriter) WriteHeader(int)       {}
func (*networkFailingWriter) Write([]byte) (int, error) {
	return 0, errors.New("fixture client disconnected")
}

func TestRelayDisconnectReleasesBodyWithoutDraining(t *testing.T) {
	u, _ := url.Parse("https://media.example/video.mp4")
	body := &networkTrackedBody{reader: strings.NewReader(strings.Repeat("v", 10*relayBufferSize))}
	g := &networkGuard{limit: 1 << 20, client: &http.Client{Transport: networkTestTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	})}}
	w := &networkFailingWriter{header: make(http.Header)}
	g.forward(w, httptest.NewRequest("GET", "/", nil), u, false, nil)
	if !body.closed || body.read > relayBufferSize || g.bytes.Load() != int64(body.read) {
		t.Fatal("client disconnect drained video or lost transfer accounting")
	}
}

func TestDialPublicRejectsMixedDNSBeforeConnecting(t *testing.T) {
	called := false
	_, err := dialPublic(context.Background(), "tcp", "media.example:443", func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("127.0.0.1")}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		called = true
		return nil, errors.New("unexpected dial")
	})
	if err == nil || called {
		t.Fatal("mixed public/private DNS response must be rejected before any connection")
	}
}

func TestDialPublicBoundsDNSAndAllFallbacks(t *testing.T) {
	for _, stage := range []string{"dns", "dial"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
			defer cancel()
			calls := 0
			_, err := dialPublic(ctx, "tcp", "media.example:443", func(ctx context.Context, network, host string) ([]net.IP, error) {
				if network != "ip" || host != "media.example" {
					t.Fatal("wrong resolver input")
				}
				if stage == "dns" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("8.8.8.8")}, nil
			}, func(ctx context.Context, _, _ string) (net.Conn, error) {
				calls++
				<-ctx.Done()
				return nil, ctx.Err()
			})
			if !errors.Is(err, context.DeadlineExceeded) || (stage == "dial" && calls != 1) || (stage == "dns" && calls != 0) {
				t.Fatalf("unbounded fallback after cancellation: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestDialPublicOrderedFallbackFreshDNSAndConnectionOwnership(t *testing.T) {
	var lookupContext context.Context
	var attempted []string
	client, peer := net.Pipe()
	defer peer.Close()
	conn, err := dialPublic(context.Background(), "tcp", "media.example:443", func(ctx context.Context, _, _ string) ([]net.IP, error) {
		lookupContext = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > networkDialTimeout || time.Until(deadline) < networkDialTimeout-time.Second {
			t.Fatal("DNS did not receive the total dial deadline")
		}
		return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("8.8.8.8")}, nil
	}, func(ctx context.Context, network, address string) (net.Conn, error) {
		if ctx != lookupContext || network != "tcp" {
			t.Fatal("fallback did not retain the DNS deadline")
		}
		attempted = append(attempted, address)
		if len(attempted) == 1 {
			return nil, errors.New("fixture first address unavailable")
		}
		return client, nil
	})
	if err != nil || strings.Join(attempted, ",") != "1.1.1.1:443,8.8.8.8:443" {
		t.Fatalf("ordered fallback: %v %v", attempted, err)
	}
	defer conn.Close()
	if !errors.Is(lookupContext.Err(), context.Canceled) {
		t.Fatal("dial deadline was not released after connection")
	}
	writeDone := make(chan error, 1)
	go func() { _, e := peer.Write([]byte("ok")); writeDone <- e }()
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	got := make([]byte, 2)
	if _, err = io.ReadFull(conn, got); err != nil || string(got) != "ok" {
		t.Fatal("canceling dial context closed established connection")
	}
	if err = <-writeDone; err != nil {
		t.Fatal(err)
	}
	// The next connection must resolve again and reject a rebound private IP.
	_, err = dialPublic(context.Background(), "tcp", "media.example:443", func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("rebound destination dialed")
		return nil, nil
	})
	if err == nil {
		t.Fatal("fresh DNS guard bypassed")
	}
}

type networkTrackedBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *networkTrackedBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *networkTrackedBody) Close() error { b.closed = true; return nil }

func TestRelayPreservesRangeAndHeadWithoutLeakingCredentials(t *testing.T) {
	payload := bytes.Repeat([]byte("v"), 4096)
	u, _ := url.Parse("https://media.example/video.mp4")
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			body := &networkTrackedBody{reader: bytes.NewReader(payload)}
			g := &networkGuard{limit: 8192, client: &http.Client{Transport: networkTestTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method != method || r.Header.Get("Range") != "bytes=4096-8191" || r.Header.Get("If-Range") != `"fixture"` || r.Header.Get("Referer") != "https://page.example/watch" {
					t.Fatal("relay changed range/validator/source headers")
				}
				if r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
					t.Fatal("browser credentials leaked to media origin")
				}
				return &http.Response{StatusCode: 206, Body: body, Header: http.Header{
					"Content-Type": {"video/mp4"}, "Content-Length": {"4096"}, "Content-Range": {"bytes 4096-8191/65536"}, "Accept-Ranges": {"bytes"}, "Etag": {`"fixture"`}, "Set-Cookie": {"must-not-leak"},
				}}, nil
			})}}
			r := httptest.NewRequest(method, "/", nil)
			r.Header.Set("Range", "bytes=4096-8191")
			r.Header.Set("If-Range", `"fixture"`)
			r.Header.Set("Cookie", "must-not-leak")
			r.Header.Set("Authorization", "must-not-leak")
			r.Header.Set("Proxy-Authorization", "must-not-leak")
			w := httptest.NewRecorder()
			g.forward(w, r, u, false, map[string]string{"Referer": "https://page.example/watch", "Cookie": "must-not-leak", "Authorization": "must-not-leak"})
			if w.Code != 206 || w.Header().Get("Content-Range") != "bytes 4096-8191/65536" || w.Header().Get("Content-Length") != "4096" || w.Header().Get("Set-Cookie") != "" || !body.closed {
				t.Fatal("relay changed range response or did not release body")
			}
			if method == http.MethodHead {
				if body.read != 0 || w.Body.Len() != 0 || g.bytes.Load() != 0 {
					t.Fatal("HEAD consumed body or transfer budget")
				}
			} else if !bytes.Equal(w.Body.Bytes(), payload) || g.bytes.Load() != int64(len(payload)) {
				t.Fatal("range bytes or byte accounting changed")
			}
		})
	}
}

func TestRelayBudgetStopsBodyWithoutDrainingOrRetrying(t *testing.T) {
	u, _ := url.Parse("https://media.example/video.mp4")
	body := &networkTrackedBody{reader: strings.NewReader(strings.Repeat("v", 10*relayBufferSize))}
	calls := 0
	g := &networkGuard{limit: 100, client: &http.Client{Transport: networkTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: body, Header: http.Header{}}, nil
	})}}
	for i := 0; i < 2; i++ {
		w := httptest.NewRecorder()
		g.forward(w, httptest.NewRequest("GET", "/", nil), u, false, nil)
		if i == 0 && w.Body.Len() > 100 || i == 1 && w.Code != 413 {
			t.Fatal("transfer limit exceeded or reset between requests")
		}
	}
	if calls != 1 || body.read > relayBufferSize || !body.closed {
		t.Fatal("budget exhaustion drained/retried a long video or leaked its body")
	}
}

func TestRelayConcurrentBuffersAreOwnedUntilResponseCompletes(t *testing.T) {
	g := &networkGuard{limit: 1 << 30, client: &http.Client{Transport: networkTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(strings.Repeat(r.URL.Query().Get("fixture"), 64<<10))), Header: http.Header{}}, nil
	})}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			value := string(rune('a' + i))
			u, _ := url.Parse("https://media.example/video?fixture=" + value)
			w := httptest.NewRecorder()
			g.forward(w, httptest.NewRequest("GET", "/", nil), u, false, nil)
			if w.Body.String() != strings.Repeat(value, 64<<10) {
				t.Error("concurrent media buffers corrupted response bytes")
			}
		}(i)
	}
	wg.Wait()
	if g.bytes.Load() != 16*(64<<10) {
		t.Fatal("concurrent transfer accounting changed")
	}
}

func TestRelayDoesNotRetryPOSTOrExposeUpstreamError(t *testing.T) {
	u, _ := url.Parse("https://media.example/extractor")
	calls := 0
	g := &networkGuard{limit: 1024, client: &http.Client{Transport: networkTestTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("private upstream detail must not reach client")
	})}}
	w := httptest.NewRecorder()
	g.forward(w, httptest.NewRequest("POST", "/", strings.NewReader("request")), u, true, nil)
	if calls != 1 || w.Code != 502 || strings.Contains(w.Body.String(), "private upstream") {
		t.Fatal("unsafe POST retry or upstream error disclosure")
	}
}

type networkCanceledBody struct {
	ctx     context.Context
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (b *networkCanceledBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.started) })
	<-b.ctx.Done()
	return 0, b.ctx.Err()
}
func (b *networkCanceledBody) Close() error { close(b.closed); return nil }

func TestGuardCloseCancelsActiveRelayBody(t *testing.T) {
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	started, closed := make(chan struct{}), make(chan struct{})
	g.client.Transport = networkTestTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: &networkCanceledBody{ctx: r.Context(), started: started, closed: closed}}, nil
	})
	relay, err := g.Relay("https://media.example/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		resp, e := http.Get(relay)
		if e == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("relay body did not start")
	}
	g.Close()
	for _, done := range []<-chan struct{}{closed, requestDone} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("guard.Close left upstream relay body running")
		}
	}
}

func startNetworkTestTunnel(t *testing.T, g *networkGuard) (net.Conn, net.Conn, <-chan struct{}) {
	t.Helper()
	client, local := net.Pipe()
	upstream, origin := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = local.Close(); _ = upstream.Close(); _ = origin.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.tunnel(g.ctx, local, upstream, bufio.NewReadWriter(bufio.NewReader(local), bufio.NewWriter(local)))
	}()
	if g.ctx.Err() == nil {
		_ = client.SetReadDeadline(time.Now().Add(time.Second))
		header := make([]byte, len("HTTP/1.1 200 Connection Established\r\n\r\n"))
		if _, err := io.ReadFull(client, header); err != nil || string(header) != "HTTP/1.1 200 Connection Established\r\n\r\n" {
			t.Fatal("CONNECT handshake did not complete")
		}
	}
	return client, origin, done
}

type networkObservedWriteConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *networkObservedWriteConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(p)
}

func TestGuardCloseCancelsBlockedCONNECTHandshake(t *testing.T) {
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	client, local := net.Pipe()
	upstream, origin := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = local.Close(); _ = upstream.Close(); _ = origin.Close() })
	started, done := make(chan struct{}), make(chan struct{})
	observed := &networkObservedWriteConn{Conn: local, started: started}
	go func() {
		defer close(done)
		g.tunnel(g.ctx, observed, upstream, bufio.NewReadWriter(bufio.NewReader(observed), bufio.NewWriter(observed)))
	}()
	// The peer deliberately never reads: the first handshake write is blocked.
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("CONNECT handshake did not start")
	}
	g.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("guard.Close could not interrupt CONNECT before handshake completed")
	}
}

func TestGuardCloseCancelsAllHijackedTunnelsAndRegistrationRace(t *testing.T) {
	for _, alreadyClosed := range []bool{false, true} {
		t.Run(strconv.FormatBool(alreadyClosed), func(t *testing.T) {
			g, err := newNetworkGuard(1024)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(g.Close)
			if alreadyClosed {
				g.Close()
			}
			var done []<-chan struct{}
			for i := 0; i < 8; i++ {
				client, origin, tunnelDone := startNetworkTestTunnel(t, g)
				done = append(done, tunnelDone)
				if !alreadyClosed {
					writeDone := make(chan error, 1)
					go func() { _, e := origin.Write([]byte("v")); writeDone <- e }()
					_ = client.SetReadDeadline(time.Now().Add(time.Second))
					var one [1]byte
					if _, err = io.ReadFull(client, one[:]); err != nil {
						t.Fatal("tunnel did not start")
					}
					if err = <-writeDone; err != nil {
						t.Fatal(err)
					}
				}
			}
			g.Close()
			for _, tunnelDone := range done {
				select {
				case <-tunnelDone:
				case <-time.After(time.Second):
					t.Fatal("guard.Close left hijacked CONNECT running for its 60-second deadline")
				}
			}
		})
	}
}

func TestTunnelBudgetStopsWithoutForwardingExcessBytes(t *testing.T) {
	g, err := newNetworkGuard(6)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)
	client, origin, done := startNetworkTestTunnel(t, g)
	writeDone := make(chan error, 1)
	go func() {
		_, e := origin.Write([]byte("12345"))
		if e == nil {
			_, e = origin.Write([]byte("67"))
		}
		writeDone <- e
	}()
	_ = client.SetReadDeadline(time.Now().Add(time.Second))
	payload, err := io.ReadAll(client)
	if err != nil || string(payload) != "12345" || g.bytes.Load() != 7 {
		t.Fatal("CONNECT exceeded or reset its transfer limit")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("budget exhaustion left tunnel copying")
	}
	<-writeDone
}

func TestSafeClientReusesFullyReadConnections(t *testing.T) {
	var connections atomic.Int64
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "fixture") }))
	s.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	s.Start()
	t.Cleanup(s.Close)
	c := safeClient()
	t.Cleanup(c.CloseIdleConnections)
	transport := c.Transport.(*http.Transport)
	// This controlled local fixture replaces only the guarded dialer; production
	// continues to resolve and pin public addresses with no ambient proxy.
	transport.DialContext = (&net.Dialer{}).DialContext
	for i := 0; i < 4; i++ {
		resp, err := c.Get(s.URL)
		if err != nil {
			t.Fatal(err)
		}
		_, err = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if connections.Load() != 1 || transport.Proxy != nil || transport.ResponseHeaderTimeout != 20*time.Second || transport.TLSHandshakeTimeout != 15*time.Second {
		t.Fatal("connection reuse or bounded/isolated transport changed")
	}
}

func TestPublicIPExcludesInternalAndTransitionNetworks(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.2.3.4", "192.168.1.2", "169.254.169.254", "100.64.2.3", "0.0.0.0", "198.18.0.1", "192.0.2.1", "240.1.2.3", "::1", "::ffff:127.0.0.1", "fd00::1", "fe80::1", "2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("allowed %s", ip)
		}
	}
	for _, ip := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(ip)) {
			t.Errorf("blocked public IP %s", ip)
		}
	}
}

func TestURLPolicyAndRedirectDowngrade(t *testing.T) {
	for _, raw := range []string{"http://example.com/video.mp4", "https://user:pass@example.com/", "https://example.com:8443/x", "file:///etc/passwd", "https://", "https://example.com%2f.internal/"} {
		if _, err := validateURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if _, err := validateURL("https://example.com/video.mp4"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "http://example.com/", nil)
	if err := safeClient().CheckRedirect(req, nil); err == nil {
		t.Fatal("allowed HTTPS downgrade")
	}
}

func TestGuardBlocksProxyAndRelayInternalConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := safeDial(ctx, "tcp", "127.0.0.1:443"); err == nil {
		conn.Close()
		t.Fatal("dialed private IP")
	}
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	relay, err := g.Relay("https://127.0.0.1/video.mp4")
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(relay)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 502 {
		t.Fatalf("relay status %d", resp.StatusCode)
	}
	r := httptest.NewRequest("CONNECT", "http://127.0.0.1:443", nil)
	r.Host = "127.0.0.1:443"
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 407 {
		t.Fatalf("unauthenticated proxy: %d", w.Code)
	}
	r.SetBasicAuth(g.token, g.token)
	r.Header.Set("Proxy-Authorization", r.Header.Get("Authorization"))
	w = httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("private CONNECT: %d", w.Code)
	}
}

func TestPythonUrllibAuthenticatesWithoutBypassingDestinationGuard(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python3 is not installed")
	}
	g, err := newNetworkGuard(1024)
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	script := `import sys,urllib.request
opener=urllib.request.build_opener(urllib.request.ProxyHandler({'https':sys.argv[1]}))
try:
    opener.open('https://127.0.0.1/',timeout=3)
except Exception as exc:
    print(str(exc))
else:
    raise RuntimeError('private destination was accessed')
`
	cmd := exec.CommandContext(ctx, python, "-c", script, g.ProxyURL())
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "LANG=C.UTF-8"}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "403") || strings.Contains(string(output), "407") {
		t.Fatalf("urllib must authenticate and then have private destination blocked: %s", output)
	}
	r := httptest.NewRequest("CONNECT", "http://127.0.0.1:443", nil)
	r.Host = "127.0.0.1:443"
	r.SetBasicAuth(g.token, "")
	r.Header.Set("Proxy-Authorization", r.Header.Get("Authorization"))
	w := httptest.NewRecorder()
	g.ServeHTTP(w, r)
	if w.Code != 407 {
		t.Fatalf("empty password accepted: %d", w.Code)
	}
}
