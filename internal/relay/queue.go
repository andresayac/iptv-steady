package relay

import (
	"context"
	"io"
	"sync"
	"time"
)

type roomState int

const (
	roomAvailable roomState = iota
	roomTimedOut
	roomCancelled
)

// queue is the read-ahead buffer between a provider and a viewer, bounded in bytes.
type queue struct {
	limit int64

	mu      sync.Mutex
	chunks  [][]byte
	queued  int64
	done    bool
	err     error
	changed chan struct{}
}

func newQueue(limit int64) *queue {
	return &queue{limit: limit, changed: make(chan struct{})}
}

func (q *queue) notify() {
	close(q.changed)
	q.changed = make(chan struct{})
}

func (q *queue) push(chunk []byte) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.chunks = append(q.chunks, chunk)
	q.queued += int64(len(chunk))
	q.notify()
}

func (q *queue) finish(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.done, q.err = true, err
	q.notify()
}

func (q *queue) buffered() int64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queued
}

// waitRoom blocks until the buffer can take more data. A timeout of zero waits indefinitely.
func (q *queue) waitRoom(ctx context.Context, timeout time.Duration) roomState {
	var expired <-chan time.Time
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		expired = timer.C
	}
	for {
		q.mu.Lock()
		hasRoom, changed := q.queued < q.limit, q.changed
		q.mu.Unlock()
		if hasRoom {
			return roomAvailable
		}
		select {
		case <-ctx.Done():
			return roomCancelled
		case <-expired:
			return roomTimedOut
		case <-changed:
		}
	}
}

// pop returns the next chunk, io.EOF after a clean end, or the error the stream failed with.
func (q *queue) pop(ctx context.Context) ([]byte, error) {
	for {
		q.mu.Lock()
		if len(q.chunks) > 0 {
			chunk := q.chunks[0]
			q.chunks[0] = nil
			q.chunks = q.chunks[1:]
			q.queued -= int64(len(chunk))
			q.notify()
			q.mu.Unlock()
			return chunk, nil
		}
		if q.done {
			err := q.err
			q.mu.Unlock()
			if err == nil {
				return nil, io.EOF
			}
			return nil, err
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}
