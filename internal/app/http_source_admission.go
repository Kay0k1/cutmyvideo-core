package app

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
)

var (
	errSourceLimit      = &sourceProblem{"source_limit", "This session has reached its source limit; wait for older sources to expire"}
	errSourceBusy       = &sourceProblem{"source_busy", "Wait for the current source preparation or cancel it before opening another"}
	errSourceStorage    = &sourceProblem{"storage_limit", "Source storage is full; use a smaller file or wait for older files to expire"}
	errSourceServerBusy = &sourceProblem{"server_busy", "The server is preparing other sources; try again shortly"}
)

func (s *Server) beginSource(ctx context.Context, owner string, thumbnails ...bool) error {
	return s.beginSourceWithBytes(ctx, owner, s.Config.MaxSourceBytes, thumbnails...)
}

func (s *Server) beginSourceWithBytes(ctx context.Context, owner string, bytes int64, thumbnails ...bool) error {
	return s.beginSourceReservation(ctx, owner, func(dbCtx context.Context) error {
		return s.Store.reserveSource(dbCtx, s.Config, owner, s.sourceToken(owner), len(thumbnails) > 0 && thumbnails[0], bytes)
	})
}

func (s *Server) beginSourceReservation(ctx context.Context, owner string, reserve func(context.Context) error) error {
	// Reserve the owner's preparation slot before reading quota state. A second
	// request must not carry an old count/size past the first request's completion.
	s.mu.Lock()
	if s.preparing[owner] {
		s.mu.Unlock()
		return errSourceBusy
	}
	select {
	case s.slots <- struct{}{}:
		s.preparing[owner] = true
		s.preparationTokens[owner] = newID("prep")
		s.mu.Unlock()
	default:
		s.mu.Unlock()
		return errSourceServerBusy
	}
	accepted := false
	defer func() {
		if !accepted {
			s.endSource(owner)
		}
	}()
	if err := apiDatabaseExec(ctx, reserve); err != nil {
		return err
	}
	accepted = true
	return nil
}

func sourceTimedOut(ctx context.Context, err error) bool {
	var networkError net.Error
	return errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) && networkError.Timeout()
}

func writeSourceAdmissionError(w http.ResponseWriter, ctx context.Context, err error) {
	if errors.Is(err, errDatabaseUnavailable) {
		internalError(w, err)
	} else if sourceTimedOut(ctx, err) {
		writeError(w, 504, "source_timeout", "Source preparation timed out; try again")
	} else if problem := problemFromError(err); problem != nil {
		writeError(w, 429, problem.code, problem.message)
	} else {
		internalError(w, err)
	}
}

func (s *Server) endSource(owner string) {
	ctx, cancel := context.WithTimeout(context.Background(), workerDatabaseTimeout)
	defer cancel()
	s.mu.Lock()
	held := s.uncertainPreparations[owner]
	s.mu.Unlock()
	if err := func() error {
		if held {
			return nil
		}
		return s.Store.FinishSource(ctx, s.Config, owner, s.sourceToken(owner), s.sourceIdentity(owner, ""))
	}(); err != nil {
		slog.Error("source reservation release failed", "error", err)
	}
	s.mu.Lock()
	delete(s.preparing, owner)
	delete(s.preparationTokens, owner)
	delete(s.preparationSources, owner)
	delete(s.uncertainPreparations, owner)
	s.mu.Unlock()
	<-s.slots
}

func (s *Server) sourceToken(owner string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.preparationTokens[owner]
}

func (s *Server) holdSourceReservation(owner string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.uncertainPreparations[owner] = true
}

func (s *Server) sourceIdentity(owner, id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != "" {
		s.preparationSources[owner] = id
	}
	return s.preparationSources[owner]
}
