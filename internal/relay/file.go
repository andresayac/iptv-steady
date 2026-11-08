package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"slices"
	"strconv"
	"time"

	"iptv-steady/internal/events"
)

// fileSession serves one HTTP request for a movie or an episode. After start, its state belongs to the pump goroutine.
type fileSession struct {
	playback
	want  byteRange
	queue *queue

	source      Source
	contentType string
	position    int64
	end         int64
	total       int64

	tail          []byte
	notIdentical  map[Source]bool
	episodeStart  time.Time
	attempts      int
	sinceRecovery int64
}

func (e *Engine) ServeFile(w http.ResponseWriter, r *http.Request, viewer Viewer, media Media) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	s := &fileSession{
		playback:     playback{engine: e, viewer: viewer, media: media},
		want:         parseRange(r.Header.Get("Range")),
		queue:        newQueue(int64(e.file.ReadAhead)),
		notIdentical: map[Source]bool{},
	}
	up, err := s.start(ctx)
	if err != nil {
		s.refuse(ctx, w, err)
		return
	}
	s.writeHeaders(w)
	if r.Method == http.MethodHead {
		up.close()
		return
	}
	pumped := make(chan struct{})
	go func() {
		defer close(pumped)
		s.pump(ctx, up)
	}()
	failed := s.deliver(ctx, w)
	cancel()
	<-pumped
	if failed {
		panic(http.ErrAbortHandler)
	}
}

func (s *fileSession) bindingKey() string {
	return fmt.Sprintf("%s|%s|%d", s.viewer.User, s.media.Kind, s.media.TitleID)
}

func (s *fileSession) start(ctx context.Context) (*upstream, error) {
	e := s.engine
	bound, isBound := e.bindings.Get(s.bindingKey())
	var preferred *Source
	if isBound {
		preferred = &bound.source
	}
	mustMatch := isBound && s.want.continuesFile()
	var last error = errNoSources
	for pass := range e.file.OpenPasses {
		if pass > 0 && !sleep(ctx, delayFor(e.file.RetryDelays, pass)) {
			return nil, ctx.Err()
		}
		for _, source := range e.ordered(s.media, preferred) {
			up, err := e.open(ctx, s.firstRequest(source))
			if err == nil && mustMatch && up.total != bound.total {
				up.close()
				err = errDifferentFile
			}
			if err == nil {
				s.adopt(up)
				return up, nil
			}
			if ctx.Err() != nil || errors.Is(err, errUnsatisfiable) {
				return nil, err
			}
			last = err
			s.noteOpenFailure(source, err)
		}
	}
	return nil, last
}

func (s *fileSession) firstRequest(source Source) openRequest {
	return openRequest{
		kind: s.media.Kind, source: source, extension: source.Extension,
		rangeHeader: s.want.header(), offset: s.want.start, suffix: s.want.suffix > 0,
		timeout: s.engine.file.OpenTimeout, bufferSize: int(s.engine.file.ChunkSize),
	}
}

func (s *fileSession) noteOpenFailure(source Source, err error) {
	kind := events.Rejected
	if errors.Is(err, errDeadLink) {
		kind = events.DeadLink
		s.engine.markDead(s.media.Kind, source)
	}
	s.record(events.Event{Kind: kind, Account: source.Account, Detail: err.Error()})
}

func (s *fileSession) adopt(up *upstream) {
	s.source, s.contentType = up.source, up.contentType
	s.position, s.end, s.total = up.start, up.end, up.total
	if s.want.end >= 0 && (s.end < 0 || s.want.end < s.end) {
		s.end = s.want.end
	}
	s.engine.bindings.Set(s.bindingKey(), binding{up.source, up.total}, s.engine.file.StickyTTL)
}

func (s *fileSession) refuse(ctx context.Context, w http.ResponseWriter, err error) {
	switch {
	case ctx.Err() != nil:
	case errors.Is(err, errUnsatisfiable):
		http.Error(w, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
	default:
		s.record(events.Event{Kind: events.GaveUp, Detail: err.Error()})
		http.Error(w, "no working source", http.StatusBadGateway)
	}
}

func (s *fileSession) writeHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", s.resolvedContentType())
	header.Set("Accept-Ranges", "bytes")
	header.Set("Cache-Control", "no-store")
	if s.end >= 0 {
		header.Set("Content-Length", strconv.FormatInt(s.end-s.position+1, 10))
	}
	status := http.StatusOK
	if s.want.present && s.total >= 0 {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", s.position, s.end, s.total))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
}

func (s *fileSession) resolvedContentType() string {
	if s.contentType != "" {
		return s.contentType
	}
	if guessed := mime.TypeByExtension("." + s.source.Extension); guessed != "" {
		return guessed
	}
	return "application/octet-stream"
}

// deliver writes buffered data to the viewer and reports whether the stream ended in failure.
func (s *fileSession) deliver(ctx context.Context, w http.ResponseWriter) bool {
	controller := http.NewResponseController(w)
	for {
		chunk, err := s.queue.pop(ctx)
		if errors.Is(err, io.EOF) {
			return false
		}
		if err != nil {
			return ctx.Err() == nil
		}
		_ = controller.SetWriteDeadline(time.Now().Add(s.engine.file.ClientIdleTimeout))
		if _, err := w.Write(chunk); err != nil {
			return false
		}
		_ = controller.Flush()
	}
}

func (s *fileSession) pump(ctx context.Context, up *upstream) {
	for {
		err := s.transfer(ctx, up)
		up.close()
		if err == nil || ctx.Err() != nil {
			s.queue.finish(ctx.Err())
			return
		}
		if up, err = s.recover(ctx, err); err != nil {
			s.queue.finish(err)
			return
		}
	}
}

func (s *fileSession) finished() bool {
	return s.end >= 0 && s.position > s.end
}

// slowDetector only counts time spent waiting on the provider, so a paused viewer never looks like a slow provider.
type slowDetector struct {
	window   time.Duration
	minRate  float64
	waited   time.Duration
	received int64
}

func (d *slowDetector) add(received int, took time.Duration) bool {
	d.waited += took
	d.received += int64(received)
	if d.waited < d.window {
		return false
	}
	slow := float64(d.received)/d.waited.Seconds() < d.minRate
	d.waited, d.received = 0, 0
	return slow
}

func (s *fileSession) transfer(ctx context.Context, up *upstream) error {
	cfg := s.engine.file
	buffer := make([]byte, cfg.ChunkSize)
	detector := slowDetector{window: cfg.SlowWindow, minRate: float64(cfg.SlowMinRate)}
	for !s.finished() {
		switch s.queue.waitRoom(ctx, cfg.ParkAfter) {
		case roomTimedOut:
			return errParked
		case roomCancelled:
			return ctx.Err()
		}
		began := time.Now()
		n, err := up.read(buffer, cfg.StallTimeout)
		if n > 0 {
			s.accept(buffer[:n])
		}
		switch {
		case s.finished():
			return nil
		case errors.Is(err, io.EOF) && s.end < 0:
			return nil
		case err != nil:
			return classifyRead(err)
		case detector.add(n, time.Since(began)):
			return errSlow
		}
	}
	return nil
}

func classifyRead(err error) error {
	if errors.Is(err, errStalled) {
		return err
	}
	return fmt.Errorf("%w: %v", errDropped, err)
}

func (s *fileSession) accept(data []byte) {
	if s.end >= 0 {
		data = data[:min(int64(len(data)), s.end-s.position+1)]
	}
	s.queue.push(bytes.Clone(data))
	s.position += int64(len(data))
	s.rememberTail(data)
	s.sinceRecovery += int64(len(data))
	if !s.episodeStart.IsZero() && s.sinceRecovery >= int64(s.engine.file.ProgressBytes) {
		s.episodeStart, s.attempts = time.Time{}, 0
	}
}

func (s *fileSession) rememberTail(data []byte) {
	limit := int(s.engine.file.TailCheck)
	if len(data) >= limit {
		s.tail = append(s.tail[:0], data[len(data)-limit:]...)
		return
	}
	s.tail = append(s.tail, data...)
	if excess := len(s.tail) - limit; excess > 0 {
		s.tail = append(s.tail[:0], s.tail[excess:]...)
	}
}

func eventKindOf(cause error) events.Kind {
	switch {
	case errors.Is(cause, errStalled):
		return events.Stalled
	case errors.Is(cause, errSlow):
		return events.Slow
	case errors.Is(cause, errParked):
		return events.Parked
	}
	return events.Dropped
}

// recover reopens the stream at the exact next byte: first on the same copy, then on any copy proven to be the same file.
func (s *fileSession) recover(ctx context.Context, cause error) (*upstream, error) {
	cfg := s.engine.file
	s.record(events.Event{Kind: eventKindOf(cause), Account: s.source.Account, Position: s.position, Buffered: s.queue.buffered(), Detail: cause.Error()})
	if errors.Is(cause, errParked) && s.queue.waitRoom(ctx, 0) != roomAvailable {
		return nil, ctx.Err()
	}
	began := time.Now()
	if s.episodeStart.IsZero() || errors.Is(cause, errParked) {
		s.episodeStart, s.attempts = began, 0
	}
	deadline := s.episodeStart.Add(cfg.RecoveryBudget)
	var last error = errNoSources
	for time.Now().Before(deadline) {
		source, found := s.nextRecoverySource()
		if !found {
			break
		}
		if !sleep(ctx, delayFor(cfg.RetryDelays, s.attempts)) {
			return nil, ctx.Err()
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		s.attempts++
		up, err := s.reopen(ctx, source, min(remaining, cfg.ReopenTimeout))
		if err == nil {
			s.resumed(up, cause, began)
			return up, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		last = err
		if errors.Is(err, errDifferentFile) {
			s.notIdentical[source] = true
		}
	}
	s.record(events.Event{Kind: events.GaveUp, Account: s.source.Account, Position: s.position, DowntimeMS: time.Since(began).Milliseconds(), Detail: last.Error()})
	return nil, fmt.Errorf("gave up after %s: %w", time.Since(began).Round(time.Millisecond), last)
}

func (s *fileSession) recoveryOrder() []Source {
	order := slices.Repeat([]Source{s.source}, s.engine.file.RetriesPerCopy)
	for _, source := range s.engine.ordered(s.media, nil) {
		if source != s.source && !s.notIdentical[source] {
			order = append(order, source)
		}
	}
	return order
}

// nextRecoverySource advances across recoveries of one episode, so a copy that keeps failing right after reconnecting is left behind.
func (s *fileSession) nextRecoverySource() (Source, bool) {
	order := s.recoveryOrder()
	if len(order) == 0 {
		return Source{}, false
	}
	return order[s.attempts%len(order)], true
}

func (s *fileSession) reopen(ctx context.Context, source Source, timeout time.Duration) (*upstream, error) {
	same := source == s.source
	from := s.position
	if !same {
		from -= int64(len(s.tail))
	}
	up, err := s.engine.open(ctx, openRequest{
		kind: s.media.Kind, source: source, extension: source.Extension,
		rangeHeader: rangeHeader(from, s.end), offset: from,
		timeout: timeout, bufferSize: int(s.engine.file.ChunkSize),
	})
	if err != nil {
		return nil, err
	}
	if s.total >= 0 && up.total != s.total {
		up.close()
		return nil, errDifferentFile
	}
	if !same {
		if err := s.verifyTail(up, timeout); err != nil {
			up.close()
			return nil, err
		}
	}
	return up, nil
}

// verifyTail proves another account's file is byte-identical by comparing the data delivered just before the resume point.
func (s *fileSession) verifyTail(up *upstream, timeout time.Duration) error {
	if len(s.tail) == 0 {
		return nil
	}
	received := make([]byte, len(s.tail))
	if err := up.readFull(received, timeout); err != nil {
		return err
	}
	if !bytes.Equal(received, s.tail) {
		return errDifferentFile
	}
	return nil
}

func (s *fileSession) resumed(up *upstream, cause error, began time.Time) {
	if up.source != s.source {
		s.record(events.Event{Kind: events.SwitchedCopy, Account: up.source.Account, Position: s.position, Detail: "from " + s.source.Account})
		s.source, s.attempts = up.source, 0
		s.engine.bindings.Set(s.bindingKey(), binding{up.source, s.total}, s.engine.file.StickyTTL)
	}
	s.sinceRecovery = 0
	if !errors.Is(cause, errParked) {
		s.record(events.Event{Kind: events.Recovered, Account: up.source.Account, Position: s.position, Buffered: s.queue.buffered(), DowntimeMS: time.Since(began).Milliseconds()})
	}
}
