package app

import (
	"context"
	"errors"
	"math"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

const platformMetadataCacheTTL = 5 * time.Minute
const platformMetadataCacheLimit = 1 << 20
const platformMetadataExpiryMargin = 30 * time.Second
const platformMetadataDBTimeout = 2 * time.Second
const platformMetadataResolutionLeaseTTL = 3 * time.Minute

var errMetadataNotCacheable = errors.New("source metadata is not cacheable")

// ResolvePlatformMetadata shares an owner's recent upstream metadata between
// the API and workers. It never caches a guard's process-local relay address.
// Workers must still use a new guard for every upstream connection.
func (s *Store) ResolvePlatformMetadata(ctx context.Context, c Config, source Source, g *networkGuard) (platformInfo, bool, error) {
	return s.resolvePlatformMetadata(ctx, source, func() (platformInfo, error) {
		return platformMetadata(ctx, c, source.URL, g)
	})
}

// ForceFreshPlatformMetadata bypasses cache reads after an upstream rejection.
// The original guard still owns the transfer budget for both attempts.
func (s *Store) ForceFreshPlatformMetadata(ctx context.Context, c Config, source Source, g *networkGuard) (platformInfo, error) {
	return s.forceFreshPlatformMetadata(ctx, source, func() (platformInfo, error) {
		return platformMetadata(ctx, c, source.URL, g)
	})
}

func (s *Store) forceFreshPlatformMetadata(ctx context.Context, source Source, resolve func() (platformInfo, error)) (platformInfo, error) {
	authCtx, cancelAuth := context.WithTimeout(ctx, platformMetadataDBTimeout)
	err := s.authorizePlatformMetadataSource(authCtx, source)
	cancelAuth()
	if err != nil {
		return platformInfo{}, err
	}
	_ = s.InvalidatePlatformMetadata(ctx, source)
	claimCtx, cancelClaim := context.WithTimeout(ctx, platformMetadataDBTimeout)
	token := newID("metadata")
	claimed, _ := s.claimPlatformMetadataResolution(claimCtx, source, token)
	cancelClaim()
	if claimed {
		defer s.releasePlatformMetadataResolution(source, token)
	}
	// A force refresh always obtains fresh upstream data. Cache coordination is
	// optional, but only a claimed token may publish after this network phase.
	info, err := freshPlatformMetadata(source, resolve)
	if err == nil && claimed {
		_ = s.cacheResolvedPlatformMetadata(ctx, source, token, info)
	}
	return info, err
}

func forceFreshPlatformMetadata(ctx context.Context, c Config, source Source, g *networkGuard) (platformInfo, error) {
	return freshPlatformMetadata(source, func() (platformInfo, error) {
		return platformMetadata(ctx, c, source.URL, g)
	})
}

func freshPlatformMetadata(source Source, resolve func() (platformInfo, error)) (platformInfo, error) {
	info, err := resolve()
	if err != nil {
		return info, err
	}
	if err = validatePlatformInfo(info); err != nil {
		return platformInfo{}, err
	}
	if info.ID != source.ProviderID || math.Abs(info.Duration*1000-float64(source.DurationMS)) > 1000 {
		return platformInfo{}, &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	info.cachedUntil = time.Time{}
	return info, nil
}

func (s *Store) authorizePlatformMetadataSource(ctx context.Context, source Source) error {
	var stored Source
	err := s.DB.QueryRow(ctx, `SELECT url,path,provider_id,duration_ms FROM sources WHERE id=$1 AND owner=$2`, source.ID, source.Owner).
		Scan(&stored.URL, &stored.Path, &stored.ProviderID, &stored.DurationMS)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if stored.URL != source.URL || stored.Path != source.Path || stored.ProviderID != source.ProviderID || stored.DurationMS != source.DurationMS {
		return &sourceProblem{"source_changed", "The source has changed; open it again before exporting"}
	}
	if _, err = validateURL(source.URL); err != nil {
		return errPlatformUnavailable
	}
	return nil
}

func (s *Store) resolvePlatformMetadata(ctx context.Context, source Source, resolve func() (platformInfo, error)) (platformInfo, bool, error) {
	// Authorize independently of a cache hit, including after a source expires.
	cacheCtx, cancelCache := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancelCache()
	if err := s.authorizePlatformMetadataSource(cacheCtx, source); err != nil {
		return platformInfo{}, false, err
	}
	if info, ok := readPlatformMetadataCache(cacheCtx, s.DB, source); ok {
		return info, true, nil
	}
	// A persisted, expiring claim coordinates processes while every SQL call
	// releases its connection before upstream work. Lease heartbeats and queue
	// claims must remain able to use the small shared pool during slow extraction.
	token := newID("metadata")
	for {
		claimed, err := s.claimPlatformMetadataResolution(cacheCtx, source, token)
		if err != nil || cacheCtx.Err() != nil {
			if ctx.Err() != nil {
				return platformInfo{}, false, ctx.Err()
			}
			// Cache coordination remains optional. A bounded wait preserves the
			// previous independent-resolution fallback during outages/slow peers.
			info, err := resolve()
			return info, false, err
		}
		if claimed {
			defer s.releasePlatformMetadataResolution(source, token)
			if info, ok := readPlatformMetadataCache(cacheCtx, s.DB, source); ok {
				return info, true, nil
			}
			cancelCache()
			info, err := resolve()
			if err == nil {
				_ = s.cacheResolvedPlatformMetadata(ctx, source, token, info)
			}
			return info, false, err
		}
		if info, ok := readPlatformMetadataCache(cacheCtx, s.DB, source); ok {
			return info, true, nil
		}
		timer := time.NewTimer(50 * time.Millisecond)
		select {
		case <-cacheCtx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (s *Store) claimPlatformMetadataResolution(ctx context.Context, source Source, token string) (bool, error) {
	var claimed string
	err := s.DB.QueryRow(ctx, `INSERT INTO source_metadata_resolutions(source_id,owner,token,expires_at)
 SELECT id,owner,$3,clock_timestamp()+($7*interval '1 second') FROM sources
 WHERE id=$1 AND owner=$2 AND url=$4 AND path='' AND provider_id=$5 AND duration_ms=$6 AND NOT media_removed
 ON CONFLICT(source_id) DO UPDATE SET token=EXCLUDED.token,expires_at=EXCLUDED.expires_at
 WHERE source_metadata_resolutions.owner=EXCLUDED.owner AND source_metadata_resolutions.expires_at<=clock_timestamp()
 RETURNING token`, source.ID, source.Owner, token, source.URL, source.ProviderID, source.DurationMS, platformMetadataResolutionLeaseTTL.Seconds()).Scan(&claimed)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil && claimed == token, err
}

func (s *Store) releasePlatformMetadataResolution(source Source, token string) {
	ctx, cancel := context.WithTimeout(context.Background(), platformMetadataDBTimeout)
	defer cancel()
	_, _ = s.DB.Exec(ctx, `DELETE FROM source_metadata_resolutions WHERE source_id=$1 AND owner=$2 AND token=$3`, source.ID, source.Owner, token)
}

func (s *Store) cacheResolvedPlatformMetadata(parent context.Context, source Source, token string, info platformInfo) error {
	ctx, cancel := context.WithTimeout(parent, platformMetadataDBTimeout)
	defer cancel()
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	var owned bool
	err = tx.QueryRow(ctx, `SELECT expires_at>clock_timestamp() FROM source_metadata_resolutions WHERE source_id=$1 AND owner=$2 AND token=$3 FOR UPDATE`, source.ID, source.Owner, token).Scan(&owned)
	if err != nil || !owned {
		return err
	}
	if err = writePlatformMetadataCache(ctx, tx, source, info, time.Now()); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `DELETE FROM source_metadata_resolutions WHERE source_id=$1 AND owner=$2 AND token=$3`, source.ID, source.Owner, token); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) CachePlatformMetadata(ctx context.Context, source Source, info platformInfo) error {
	return writePlatformMetadataCache(ctx, s.DB, source, info, time.Now())
}

// RecentPlatformMetadata reuses a recent import only for its original owner
// and canonical page URL. Copied source records retain the original deadline.
func (s *Store) RecentPlatformMetadata(ctx context.Context, owner, raw string) (platformInfo, bool) {
	if owner == "" {
		return platformInfo{}, false
	}
	if _, err := validateURL(raw); err != nil {
		return platformInfo{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancel()
	var source Source
	source.Owner, source.URL = owner, raw
	var payload []byte
	var expires time.Time
	err := s.DB.QueryRow(ctx, `SELECT source.id,source.provider_id,source.duration_ms,cache.payload,cache.expires_at FROM sources source
 JOIN source_metadata_cache cache ON cache.source_id=source.id
 WHERE source.owner=$1 AND cache.owner=$1 AND source.url=$2 AND source.path='' AND cache.expires_at>now()
 ORDER BY cache.expires_at DESC LIMIT 1`, owner, raw).Scan(&source.ID, &source.ProviderID, &source.DurationMS, &payload, &expires)
	if err != nil {
		return platformInfo{}, false
	}
	return cachedPlatformMetadata(source, payload, expires)
}

func (s *Store) InvalidatePlatformMetadata(ctx context.Context, source Source) error {
	ctx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancel()
	return s.invalidatePlatformMetadataRows(ctx,
		`DELETE FROM source_metadata_resolutions WHERE source_id=$1 AND owner=$2`,
		`DELETE FROM source_metadata_cache WHERE source_id=$1 AND owner=$2`, source.ID, source.Owner)
}

func (s *Store) invalidateRecentPlatformMetadata(ctx context.Context, owner, raw string) {
	ctx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancel()
	_ = s.invalidatePlatformMetadataRows(ctx,
		`DELETE FROM source_metadata_resolutions resolution USING sources source
 WHERE resolution.source_id=source.id AND resolution.owner=$1 AND source.owner=$1 AND source.url=$2`,
		`DELETE FROM source_metadata_cache cache USING sources source
 WHERE cache.source_id=source.id AND cache.owner=$1 AND source.owner=$1 AND source.url=$2`, owner, raw)
}

func (s *Store) invalidatePlatformMetadataRows(ctx context.Context, leaseSQL, cacheSQL string, args ...any) error {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer rollbackStorage(tx)
	// A publisher may hold the lease while inserting a previously absent cache.
	// Wait for that transaction first, then use a fresh READ COMMITTED statement
	// snapshot so its newly committed cache is visible to the second DELETE.
	if _, err = tx.Exec(ctx, leaseSQL, args...); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, cacheSQL, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func readPlatformMetadataCache(ctx context.Context, db dbExecutor, source Source) (platformInfo, bool) {
	ctx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancel()
	var payload []byte
	var expires time.Time
	err := db.QueryRow(ctx, `SELECT cache.payload,cache.expires_at FROM source_metadata_cache cache JOIN sources source ON source.id=cache.source_id
 WHERE cache.source_id=$1 AND cache.owner=$2 AND source.owner=$2 AND source.url=$3 AND source.path=''
 AND source.provider_id=$4 AND source.duration_ms=$5 AND cache.expires_at>now()`,
		source.ID, source.Owner, source.URL, source.ProviderID, source.DurationMS).Scan(&payload, &expires)
	if err != nil {
		return platformInfo{}, false
	}
	return cachedPlatformMetadata(source, payload, expires)
}

func cachedPlatformMetadata(source Source, payload []byte, expires time.Time) (platformInfo, bool) {
	info, ok := decodePlatformMetadata(payload)
	if !ok {
		return platformInfo{}, false
	}
	info.cachedUntil = expires
	filtered, _, err := cacheablePlatformMetadata(source, info, time.Now())
	// An expired or newly invalid rendition must force a fresh resolution,
	// rather than silently changing the available export qualities.
	return filtered, err == nil && len(filtered.Formats) == len(info.Formats)
}

func writePlatformMetadataCache(ctx context.Context, db dbExecutor, source Source, info platformInfo, now time.Time) error {
	info, expires, err := cacheablePlatformMetadata(source, info, now)
	if err != nil {
		return err
	}
	payload, err := encodePlatformMetadata(info)
	if err != nil {
		return errMetadataNotCacheable
	}
	ctx, cancel := context.WithTimeout(ctx, platformMetadataDBTimeout)
	defer cancel()
	_, err = db.Exec(ctx, `INSERT INTO source_metadata_cache(source_id,owner,payload,expires_at)
 SELECT id,owner,$3,$4 FROM sources WHERE id=$1 AND owner=$2 AND path='' AND url=$5 AND provider_id=$6 AND duration_ms=$7
 ON CONFLICT(source_id) DO UPDATE SET payload=EXCLUDED.payload,expires_at=EXCLUDED.expires_at
 WHERE source_metadata_cache.owner=EXCLUDED.owner`, source.ID, source.Owner, payload, expires, source.URL, source.ProviderID, source.DurationMS)
	return err
}

func cacheablePlatformMetadata(source Source, info platformInfo, now time.Time) (platformInfo, time.Time, error) {
	if source.ID == "" || source.Owner == "" || source.Path != "" || source.ProviderID != info.ID ||
		validatePlatformInfo(info) != nil || math.Abs(info.Duration*1000-float64(source.DurationMS)) > 1000 {
		return platformInfo{}, time.Time{}, errMetadataNotCacheable
	}
	if _, err := validateURL(source.URL); err != nil {
		return platformInfo{}, time.Time{}, errMetadataNotCacheable
	}
	expires := now.Add(platformMetadataCacheTTL)
	if !info.cachedUntil.IsZero() {
		if !info.cachedUntil.After(now) {
			return platformInfo{}, time.Time{}, errMetadataNotCacheable
		}
		if info.cachedUntil.Before(expires) {
			expires = info.cachedUntil
		}
	}
	formats := make([]platformFormat, 0, len(info.Formats))
	for _, format := range info.Formats {
		if format.HasDRM || (format.Protocol != "https" && !isHLS(format)) {
			continue
		}
		upstream, err := validateURL(format.URL)
		if err != nil || upstream.Hostname() == "localhost" {
			continue
		}
		if ip := net.ParseIP(upstream.Hostname()); ip != nil && !publicIP(ip) {
			continue
		}
		deadline, known, valid := signedMetadataExpiry(upstream)
		if !valid {
			continue
		}
		if known {
			deadline = deadline.Add(-platformMetadataExpiryMargin)
			if !deadline.After(now) {
				continue
			}
			if deadline.Before(expires) {
				expires = deadline
			}
		}
		formats = append(formats, format)
	}
	info.Formats = formats
	// Collection envelopes cannot enter the cache. Keep the private thumbnail
	// URL for repeated imports; its fetch still uses a fresh network guard.
	info.Entries = nil
	if _, err := pickStreams(info, "best", "mp4"); err != nil {
		if _, err = pickStreams(info, "best", "mp3"); err != nil {
			return platformInfo{}, time.Time{}, errMetadataNotCacheable
		}
	}
	return info, expires, nil
}

// Recognized absolute and duration-based signing fields bound cache lifetime.
// Unrecognized provider tokens retain the short TTL and a worker access retry.
func signedMetadataExpiry(u *url.URL) (time.Time, bool, bool) {
	query := u.Query()
	var earliest time.Time
	known := false
	add := func(value time.Time) {
		if !known || value.Before(earliest) {
			earliest = value
		}
		known = true
	}
	for _, key := range []string{"expire", "expires", "exp"} {
		if values, present := query[key]; present {
			for _, value := range values {
				seconds, err := strconv.ParseInt(value, 10, 64)
				if err != nil || seconds <= 0 {
					return time.Time{}, true, false
				}
				add(time.Unix(seconds, 0))
			}
		}
	}
	for _, prefix := range []string{"X-Amz-", "X-Goog-"} {
		if query.Has(prefix + "Expires") {
			created, err := time.Parse("20060102T150405Z", query.Get(prefix+"Date"))
			if err != nil {
				return time.Time{}, true, false
			}
			seconds, err := strconv.ParseInt(query.Get(prefix+"Expires"), 10, 64)
			if err != nil || seconds <= 0 || seconds > 7*24*3600 {
				return time.Time{}, true, false
			}
			add(created.Add(time.Duration(seconds) * time.Second))
		}
	}
	return earliest, known, true
}
