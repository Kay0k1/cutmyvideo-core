package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestSourcePreparationKeyGrammarForPathsAndHeaders(t *testing.T) {
	for _, key := range []string{"", ".", "..", "agent key", " leading", "trailing ", "bad\nkey", "é", strings.Repeat("a", 129)} {
		if validPreparationKey(key) {
			t.Errorf("accepted unusable preparation key %q", key)
		}
	}
	for _, key := range []string{"!source~", "source-key", "a..b", strings.Repeat("a", 128)} {
		if !validPreparationKey(key) {
			t.Errorf("rejected valid preparation key %q", key)
		}
	}
}

func preparationUpload(t *testing.T, media []byte, key string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "interview.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(media); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://example.com/api/v1/uploads", &body)
	sum := sha256.Sum256(media)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Idempotency-Key", key)
	r.Header.Set("Upload-SHA256", hex.EncodeToString(sum[:]))
	r.Header.Set("Upload-Size", strconv.Itoa(len(media)))
	return r
}

func TestSourcePreparationsDirectAndProviderIdentityReplay(t *testing.T) {
	_ = mediaConfig(t)
	store, c, s, _, owner := httpPreflightFixture(t)
	c.MaxStorageBytes, c.MaxOwnerBytes = 128<<20, 128<<20
	s = NewServer(c, store)
	fixture := addHTTPPreflightVideo(t, store, c, owner)
	media, err := os.ReadFile(fixture.Path)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	client := func() *http.Client {
		return &http.Client{Transport: networkTestTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if r.URL.String() != "https://media.example/interview.mp4" {
				t.Fatal("direct import changed canonical source intent")
			}
			return &http.Response{StatusCode: 200, ContentLength: int64(len(media)), Header: http.Header{"Content-Type": {"video/mp4"}}, Body: io.NopCloser(bytes.NewReader(media))}, nil
		})}
	}
	request := func(url, key string) *http.Request {
		r := httptest.NewRequest("POST", "https://example.com/api/v1/sources", strings.NewReader(`{"url":"`+url+`"}`))
		r.Header.Set("Idempotency-Key", key)
		return r
	}
	w := httptest.NewRecorder()
	s.addSourceWithClient(w, request("https://media.example/interview.mp4#ignored", "direct-key"), owner, client)
	if w.Code != 201 {
		t.Fatal("direct preparation failed", w.Code)
	}
	var source Source
	if err = json.Unmarshal(w.Body.Bytes(), &source); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(media)
	if source.Fingerprint == nil || source.Fingerprint.Kind != "content" || source.Fingerprint.SHA256 != hex.EncodeToString(sum[:]) || source.Fingerprint.SizeBytes != int64(len(media)) {
		t.Fatal("direct import fingerprint was not complete source bytes")
	}
	s = NewServer(c, store)
	s.Config.FFprobe = "/must-not-launch"
	s.Config.MaxSourceBytes = 1
	w = httptest.NewRecorder()
	s.addSourceWithClient(w, request("https://media.example/interview.mp4", "direct-key"), owner, func() *http.Client { t.Fatal("retained import opened network"); return nil })
	var replay Source
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || replay.ID != source.ID || calls != 1 {
		t.Fatal("import replay did not recover the same retained source", w.Code)
	}
	w = httptest.NewRecorder()
	s.addSourceWithClient(w, request("https://media.example/different.mp4", "direct-key"), owner, client)
	if w.Code != 409 || calls != 1 {
		t.Fatal("different import intent reused a source key")
	}
	platform, previous, _, _ := thumbnailLazyFixture(t)
	w = httptest.NewRecorder()
	platform.addSourceWithClient(w, request(previous.URL, "platform-key"), previous.Owner, func() *http.Client { t.Fatal("cached provider import fetched bytes"); return nil })
	var provider Source
	if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &provider) != nil || provider.Fingerprint == nil {
		t.Fatal("provider preparation failed", w.Code)
	}
	if provider.Fingerprint.Kind != "provider" || provider.Fingerprint.CanonicalURLSHA256 == "" || provider.Fingerprint.SHA256 != "" || provider.Fingerprint.SizeBytes != 0 {
		t.Fatal("provider identity was misrepresented as content identity")
	}
	platform = NewServer(platform.Config, platform.Store)
	platform.Config.YTDLP = "/must-not-launch"
	w = httptest.NewRecorder()
	platform.addSourceWithClient(w, request(previous.URL, "platform-key"), previous.Owner, func() *http.Client { t.Fatal("provider replay opened network"); return nil })
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &replay) != nil || replay.ID != provider.ID {
		t.Fatal("provider replay lost its retained source")
	}
}

type forbiddenPreparationBody struct{ read bool }

func (b *forbiddenPreparationBody) Read([]byte) (int, error) {
	b.read = true
	return 0, errors.New("unexpected body read")
}
func (*forbiddenPreparationBody) Close() error { return nil }

func TestSourcePreparationsUploadLostReplyReplayAndFingerprint(t *testing.T) {
	_ = mediaConfig(t)
	store, c, server, cookie, owner := httpPreflightFixture(t)
	c.MaxStorageBytes = 128 << 20
	c.MaxOwnerBytes = 128 << 20
	server = NewServer(c, store)
	fixture := addHTTPPreflightVideo(t, store, c, owner)
	media, err := os.ReadFile(fixture.Path)
	if err != nil {
		t.Fatal(err)
	}
	key := "source-recovery-key"
	r := preparationUpload(t, media, key)
	response := httptest.NewRecorder()
	server.uploadWithPublisher(response, r, owner, func(ctx context.Context, v Source) error {
		if err := store.AddSource(ctx, v); err != nil {
			return err
		}
		return errors.Join(ErrSourceCommitUncertain, errors.New("private-ack-control"))
	})
	if response.Code != 500 || strings.Contains(response.Body.String(), "private-ack-control") {
		t.Fatalf("uncertain commit response was not preserved and sanitized: status=%d body=%s", response.Code, response.Body.String())
	}
	receipt, err := store.SourcePreparation(context.Background(), owner, key)
	if err != nil || receipt.State != "ready" || receipt.Source == nil || receipt.Fingerprint == nil {
		t.Fatal("committed preparation was not recoverable", err)
	}
	sum := sha256.Sum256(media)
	if receipt.Fingerprint.Kind != "content" || receipt.Fingerprint.SHA256 != hex.EncodeToString(sum[:]) || receipt.Fingerprint.SizeBytes != int64(len(media)) {
		t.Fatal("source fingerprint does not match complete uploaded bytes")
	}
	server = NewServer(c, store)
	server.Config.FFprobe = "/must-not-launch"
	server.Config.MaxSourceBytes = 1
	request := preparationUpload(t, media, key)
	unread := &forbiddenPreparationBody{}
	request.Body = unread
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	var replay Source
	if err = json.Unmarshal(response.Body.Bytes(), &replay); err != nil || response.Code != 200 || replay.ID != receipt.SourceID || unread.read {
		t.Fatal("replay inspected media, read the body or changed the retained source")
	}
	request = preparationUpload(t, media, key)
	request.Header.Set("Upload-SHA256", strings.Repeat("0", 64))
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 409 {
		t.Fatal("changed upload intent reused a retained source key")
	}
	if _, err = store.SourcePreparation(context.Background(), "other-owner", key); !errors.Is(err, ErrNotFound) {
		t.Fatal("source preparation escaped owner scope")
	}
	var count int
	if err = store.DB.QueryRow(context.Background(), `SELECT count(*) FROM sources WHERE owner=$1`, owner).Scan(&count); err != nil || count != 2 {
		t.Fatal("source replay created duplicate resources", count, err)
	}
	server = NewServer(c, store)
	request = preparationUpload(t, media, "mismatch-key")
	request.Header.Set("Upload-SHA256", strings.Repeat("0", 64))
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != 422 || !strings.Contains(response.Body.String(), "source_fingerprint_mismatch") {
		t.Fatal("server accepted forged source fingerprint")
	}
	if err = store.DB.QueryRow(context.Background(), `SELECT count(*) FROM sources WHERE owner=$1`, owner).Scan(&count); err != nil || count != 2 {
		t.Fatal("fingerprint rejection published a source")
	}
}

func TestSourcePreparationLeaseFencingAndRetainedReceipt(t *testing.T) {
	store, c, _, _, owner := httpPreflightFixture(t)
	ctx := context.Background()
	intent := sourcePreparationIntent{Kind: "upload", SHA256: strings.Repeat("a", 64), SizeBytes: 19}
	first, err := store.claimSourcePreparation(ctx, owner, "lease-key", intent, time.Minute)
	if err != nil || first == nil {
		t.Fatal("initial preparation claim failed", err)
	}
	if duplicate, e := store.claimSourcePreparation(ctx, owner, "lease-key", intent, time.Minute); e != nil || duplicate != nil {
		t.Fatal("active preparation lease was stolen", e)
	}
	other := intent
	other.SHA256 = strings.Repeat("b", 64)
	if _, e := store.claimSourcePreparation(ctx, owner, "lease-key", other, time.Minute); !errors.Is(e, errPreparationConflict) {
		t.Fatal("different intent did not conflict")
	}
	if _, err = store.DB.Exec(ctx, `UPDATE source_preparations SET lease_until=clock_timestamp()-interval '1 second' WHERE owner=$1`, owner); err != nil {
		t.Fatal(err)
	}
	next, err := store.claimSourcePreparation(ctx, owner, "lease-key", intent, time.Minute)
	if err != nil || next == nil || next.sourceID != first.sourceID || next.token == first.token {
		t.Fatal("expired claim did not recover the stable source identity", err)
	}
	oldSource := Source{ID: first.sourceID, Owner: owner, Kind: "upload", Title: "Owned source", DurationMS: 1000, Fingerprint: &SourceFingerprint{Kind: "content", SHA256: intent.SHA256, SizeBytes: 19}}
	first.attach(&oldSource)
	oldSource.Path = writeLedgerFile(t, c, "sources", sourceFileIdentity(oldSource)+".media", 19)
	if e := store.AddSource(ctx, oldSource); !errors.Is(e, ErrNotFound) {
		t.Fatal("stale claim committed a source", e)
	}
	newSource := oldSource
	next.attach(&newSource)
	newSource.Path = writeLedgerFile(t, c, "sources", sourceFileIdentity(newSource)+".media", 19)
	if oldSource.Path == newSource.Path {
		t.Fatal("attempt cleanup can collide with a new attempt")
	}
	if err = store.AddSource(ctx, newSource); err != nil {
		t.Fatal(err)
	}
	if _, err = store.DB.Exec(ctx, `UPDATE source_preparations SET expires_at=clock_timestamp()-interval '1 day' WHERE owner=$1`, owner); err != nil {
		t.Fatal(err)
	}
	if _, err = store.claimSourcePreparation(ctx, owner, "another-key", intent, time.Minute); err != nil {
		t.Fatal(err)
	}
	receipt, err := store.SourcePreparation(ctx, owner, "lease-key")
	if err != nil || receipt.State != "ready" || receipt.Source == nil || receipt.Source.ID != first.sourceID {
		t.Fatal("TTL erased the receipt of a still-retained source", err)
	}
	if err = removeSourcePreparation(oldSource.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(newSource.Path); err != nil {
		t.Fatal("stale attempt cleanup removed the accepted source", err)
	}
}
