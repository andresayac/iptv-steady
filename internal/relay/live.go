package relay

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"iptv-steady/internal/events"
)

const liveContentType = "video/mp2t"

type liveSession struct {
	playback
	extension string
	current   *Source
	flapping  bool

	episodeStart time.Time
	attempts     int
}

// ServeLive relays a channel and reconnects, on any account that has it, while the viewer stays connected.
func (e *Engine) ServeLive(w http.ResponseWriter, r *http.Request, viewer Viewer, media Media, extension string) {
	ctx := r.Context()
	s := &liveSession{playback: playback{engine: e, viewer: viewer, media: media}, extension: extension}
	up, err := s.connect(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.record(events.Event{Kind: events.GaveUp, Detail: err.Error()})
			http.Error(w, "no working source", http.StatusBadGateway)
		}
		return
	}
	header := w.Header()
	header.Set("Content-Type", liveContentType)
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	for {
		opened := time.Now()
		err := s.forward(ctx, w, up)
		up.close()
		if ctx.Err() != nil || errors.Is(err, errClientGone) {
			return
		}
		s.record(events.Event{Kind: eventKindOf(err), Account: up.source.Account, Detail: err.Error()})
		s.noteDisconnect(time.Since(opened))
		began := time.Now()
		if up, err = s.connect(ctx); err != nil {
			s.record(events.Event{Kind: events.GaveUp, Detail: err.Error()})
			panic(http.ErrAbortHandler)
		}
		s.record(events.Event{Kind: events.Recovered, Account: up.source.Account, DowntimeMS: time.Since(began).Milliseconds()})
	}
}

// noteDisconnect keeps one recovery budget across connections that die right after opening, so a dead channel cannot loop forever.
func (s *liveSession) noteDisconnect(lived time.Duration) {
	s.flapping = lived < s.engine.live.HealthyAfter
	if !s.flapping {
		s.episodeStart, s.attempts = time.Time{}, 0
	}
	if s.episodeStart.IsZero() {
		s.episodeStart = time.Now()
	}
}

func (s *liveSession) connect(ctx context.Context) (*upstream, error) {
	cfg := s.engine.live
	if s.episodeStart.IsZero() {
		s.episodeStart = time.Now()
	}
	deadline := s.episodeStart.Add(cfg.ReconnectBudget)
	var last error = errNoSources
	for time.Now().Before(deadline) {
		order := s.order()
		if len(order) == 0 {
			break
		}
		if !sleep(ctx, delayFor(cfg.RetryDelays, s.attempts)) {
			return nil, ctx.Err()
		}
		source := order[s.attempts%len(order)]
		s.attempts++
		up, err := s.engine.open(ctx, openRequest{
			kind: s.media.Kind, source: source, extension: s.extension,
			timeout: min(cfg.OpenTimeout, max(time.Until(deadline), time.Millisecond)), bufferSize: int(cfg.ChunkSize),
		})
		if err == nil {
			s.current = &up.source
			return up, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		last = err
		if errors.Is(err, errDeadLink) {
			s.engine.markDead(s.media.Kind, source)
		}
	}
	return nil, last
}

// order leaves a source that keeps failing right after connecting for last.
func (s *liveSession) order() []Source {
	order := s.engine.ordered(s.media, s.current)
	if s.flapping && len(order) > 1 && order[0] == *s.current {
		return append(order[1:], order[0])
	}
	return order
}

func (s *liveSession) forward(ctx context.Context, w http.ResponseWriter, up *upstream) error {
	cfg := s.engine.live
	controller := http.NewResponseController(w)
	buffer := make([]byte, cfg.ChunkSize)
	for {
		n, err := up.read(buffer, cfg.StallTimeout)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				return errClientGone
			}
			_ = controller.Flush()
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return classifyRead(err)
		}
	}
}

// LiveURL returns the provider address of the best source, for formats that cannot be relayed yet.
func (e *Engine) LiveURL(media Media, extension string) (string, error) {
	order := e.ordered(media, nil)
	if len(order) == 0 {
		return "", fmt.Errorf("channel %d: %w", media.TitleID, errNoSources)
	}
	return e.pool.Get(order[0].Account).Client.StreamURL(media.Kind, order[0].ID, extension), nil
}
