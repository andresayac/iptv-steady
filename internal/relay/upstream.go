package relay

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/xtream"
)

var (
	errStalled       = errors.New("provider stopped sending data")
	errSlow          = errors.New("provider is too slow")
	errBehind        = fmt.Errorf("%w: it supplies less than the viewer consumes", errSlow)
	errDropped       = errors.New("provider closed the connection early")
	errParked        = errors.New("upstream released while the viewer is paused")
	errDeadLink      = errors.New("link is dead")
	errRejected      = errors.New("provider rejected the request")
	errUnreachable   = errors.New("provider is unreachable")
	errUnsatisfiable = errors.New("requested range is not satisfiable")
	errRangeIgnored  = errors.New("provider ignored the requested range")
	errDifferentFile = errors.New("copy is not the same file")
	errNoSources     = errors.New("no source available")
	errClientGone    = errors.New("viewer disconnected")
)

const sniffBytes = 16

var (
	contentRange   = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+|\*)$`)
	playlistHeader = []byte("#EXTM3U")
)

type openRequest struct {
	kind        xtream.Kind
	source      Source
	extension   string
	rangeHeader string
	offset      int64
	suffix      bool
	timeout     time.Duration
	bufferSize  int
}

// upstream is one open connection to a provider, holding one of the account's connection slots.
type upstream struct {
	source      Source
	body        *bufio.Reader
	closer      io.Closer
	contentType string
	start       int64
	end         int64
	total       int64

	cancel  context.CancelFunc
	release func()
	timer   *time.Timer
	expired atomic.Bool
}

func (u *upstream) expire() {
	u.expired.Store(true)
	u.cancel()
}

func (u *upstream) close() {
	u.timer.Stop()
	u.cancel()
	if u.closer != nil {
		u.closer.Close()
	}
	u.release()
}

func (u *upstream) read(buffer []byte, stall time.Duration) (int, error) {
	u.timer.Reset(stall)
	n, err := u.body.Read(buffer)
	u.timer.Stop()
	if err != nil && u.expired.Load() {
		return n, errStalled
	}
	return n, err
}

func (u *upstream) readFull(buffer []byte, limit time.Duration) error {
	u.timer.Reset(limit)
	_, err := io.ReadFull(u.body, buffer)
	u.timer.Stop()
	if err != nil && u.expired.Load() {
		return errStalled
	}
	return err
}

// isPlaylist reports whether the provider answered an HLS playlist, which can happen even for a .ts request.
func (u *upstream) isPlaylist() bool {
	head, _ := u.body.Peek(len(playlistHeader))
	return bytes.Equal(head, playlistHeader)
}

func (e *Engine) open(ctx context.Context, request openRequest) (*upstream, error) {
	account := e.pool.Get(request.source.Account)
	if account == nil {
		return nil, errNoSources
	}
	attempt, cancel := context.WithCancel(ctx)
	u := &upstream{source: request.source, cancel: cancel, release: account.Acquire()}
	u.timer = time.AfterFunc(request.timeout, u.expire)
	err := u.connect(attempt, e.pool, account, request)
	if err != nil {
		u.close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if countsAgainstAccount(err) {
			account.Failed()
		}
		return nil, err
	}
	u.timer.Stop()
	account.Succeeded()
	e.rememberSize(request.kind, request.source, u.total)
	return u, nil
}

func countsAgainstAccount(err error) bool {
	return !errors.Is(err, errDeadLink) && !errors.Is(err, errUnsatisfiable) && !errors.Is(err, errRangeIgnored)
}

func (u *upstream) connect(ctx context.Context, pool *accounts.Pool, account *accounts.Account, request openRequest) error {
	target := account.Client.StreamURL(request.kind, request.source.ID, request.extension)
	outgoing, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return account.Client.Sanitize(err)
	}
	outgoing.Header.Set("User-Agent", pool.UserAgent())
	if request.rangeHeader != "" {
		outgoing.Header.Set("Range", request.rangeHeader)
	}
	response, err := pool.StreamClient().Do(outgoing)
	if err != nil {
		if u.expired.Load() {
			return errStalled
		}
		return fmt.Errorf("%w: %v", errUnreachable, account.Client.Sanitize(err))
	}
	u.closer = response.Body
	if err := u.describe(response, request); err != nil {
		return err
	}
	u.body = bufio.NewReaderSize(response.Body, request.bufferSize)
	head, _ := u.body.Peek(sniffBytes)
	if u.expired.Load() {
		return errStalled
	}
	u.contentType = response.Header.Get("Content-Type")
	if isErrorBody(head, u.contentType) {
		return errDeadLink
	}
	return nil
}

func (u *upstream) describe(response *http.Response, request openRequest) error {
	switch response.StatusCode {
	case http.StatusOK:
		if request.offset > 0 || request.suffix {
			return errRangeIgnored
		}
		u.total, u.end = -1, -1
		if response.ContentLength >= 0 {
			u.total, u.end = response.ContentLength, response.ContentLength-1
		}
		return nil
	case http.StatusPartialContent:
		return u.describePartial(response.Header.Get("Content-Range"), request)
	case http.StatusNotFound, http.StatusGone:
		return errDeadLink
	case http.StatusRequestedRangeNotSatisfiable:
		return errUnsatisfiable
	}
	return fmt.Errorf("%w: HTTP %d", errRejected, response.StatusCode)
}

func (u *upstream) describePartial(header string, request openRequest) error {
	match := contentRange.FindStringSubmatch(header)
	if match == nil {
		return errRangeIgnored
	}
	u.start, _ = strconv.ParseInt(match[1], 10, 64)
	u.end, _ = strconv.ParseInt(match[2], 10, 64)
	u.total = -1
	if match[3] != "*" {
		u.total, _ = strconv.ParseInt(match[3], 10, 64)
	}
	if !request.suffix && u.start != request.offset {
		return errRangeIgnored
	}
	return nil
}

func isMediaHeader(head []byte) bool {
	switch {
	case len(head) >= 8 && string(head[4:8]) == "ftyp":
	case len(head) >= 4 && bytes.Equal(head[:4], []byte{0x1a, 0x45, 0xdf, 0xa3}):
	case len(head) >= 4 && string(head[:4]) == "RIFF":
	case len(head) >= 3 && string(head[:3]) == "FLV":
	case len(head) >= 4 && bytes.Equal(head[:3], []byte{0, 0, 1}) && head[3] >= 0xb0:
	case len(head) >= 1 && head[0] == 0x47:
	default:
		return false
	}
	return true
}

func isErrorBody(head []byte, contentType string) bool {
	if len(head) == 0 {
		return true
	}
	if isMediaHeader(head) {
		return false
	}
	kind := strings.ToLower(contentType)
	return strings.Contains(kind, "html") || strings.Contains(kind, "json") || strings.HasPrefix(kind, "text/plain")
}
