// Package testkit provides a scriptable fake Xtream panel for fault-injection tests.
package testkit

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	Username = "provider-user"
	Password = "provider-secret"

	writeStep         = 8 << 10
	trickleEvery      = 20 * time.Millisecond
	livePacket        = 188
	livePackets       = 7
	liveEvery         = 2 * time.Millisecond
	tsSync       byte = 0x47
)

type FaultKind int

const (
	// Stall keeps the connection open and silent once the offset is reached.
	Stall FaultKind = iota + 1
	// Cut drops the connection once the offset is reached.
	Cut
	// Status answers an HTTP error instead of the file.
	Status
	// EmptyBody answers 200 with an empty HTML body, as dead links on real panels do.
	EmptyBody
	// Trickle sends almost nothing once the offset is reached.
	Trickle
)

type Fault struct {
	Kind FaultKind
	At   int64
	Code int
}

type Request struct {
	Path  string
	Range string
}

type asset struct {
	content []byte
	live    bool
	queued  []Fault
	always  *Fault
}

// Panel is a fake provider. Faults are consumed one per request, unless set with Always.
type Panel struct {
	server *httptest.Server

	mu       sync.Mutex
	assets   map[string]*asset
	lists    map[string]string
	series   map[string]string
	limit    int
	active   int
	requests []Request
	broken   bool
}

func NewPanel(t testing.TB) *Panel {
	p := &Panel{assets: map[string]*asset{}, lists: map[string]string{}, series: map[string]string{}}
	p.server = httptest.NewServer(p)
	t.Cleanup(p.Close)
	return p
}

func (p *Panel) URL() string { return p.server.URL }

func (p *Panel) Close() {
	p.server.CloseClientConnections()
	p.server.Close()
}

func Content(size int) []byte {
	content := make([]byte, size)
	_, _ = rand.NewChaCha8([32]byte{7}).Read(content)
	return content
}

func streamPath(segment, id, extension string) string {
	return fmt.Sprintf("/%s/%s/%s/%s.%s", segment, Username, Password, id, extension)
}

func (p *Panel) add(path string, item *asset) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.assets[path] = item
	return path
}

func (p *Panel) AddMovie(id, extension string, content []byte) string {
	return p.add(streamPath("movie", id, extension), &asset{content: content})
}

func (p *Panel) AddEpisode(id, extension string, content []byte) string {
	return p.add(streamPath("series", id, extension), &asset{content: content})
}

func (p *Panel) AddChannel(id string) string {
	return p.add(streamPath("live", id, "ts"), &asset{live: true})
}

func (p *Panel) Inject(path string, faults ...Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.assets[path].queued = append(p.assets[path].queued, faults...)
}

func (p *Panel) Always(path string, fault Fault) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.assets[path].always = &fault
}

func (p *Panel) Heal(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.assets[path].always, p.assets[path].queued = nil, nil
}

func (p *Panel) SetList(action, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lists[action] = body
}

func (p *Panel) SetSeries(id, body string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.series[id] = body
}

// SetBroken makes the API answer 500, as a provider outage does.
func (p *Panel) SetBroken(broken bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.broken = broken
}

// SetLimit makes the panel refuse streams beyond this many at once.
func (p *Panel) SetLimit(limit int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.limit = limit
}

func (p *Panel) Active() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.active
}

func (p *Panel) Requests(path string) []Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []Request
	for _, request := range p.requests {
		if request.Path == path {
			out = append(out, request)
		}
	}
	return out
}

func (p *Panel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/player_api.php" {
		p.serveAPI(w, r)
		return
	}
	item, fault, allowed := p.admit(r)
	if !allowed {
		http.Error(w, "too many connections", http.StatusForbidden)
		return
	}
	defer p.leave()
	switch {
	case item == nil:
		http.NotFound(w, r)
	case item.live:
		serveLive(w, r, fault)
	default:
		serveFile(w, r, item.content, fault)
	}
}

func (p *Panel) admit(r *http.Request) (*asset, Fault, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, Request{r.URL.Path, r.Header.Get("Range")})
	if p.limit > 0 && p.active >= p.limit {
		return nil, Fault{}, false
	}
	p.active++
	item := p.assets[r.URL.Path]
	var fault Fault
	switch {
	case item == nil:
	case len(item.queued) > 0:
		fault, item.queued = item.queued[0], item.queued[1:]
	case item.always != nil:
		fault = *item.always
	}
	return item, fault, true
}

func (p *Panel) leave() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.active--
}

func (p *Panel) serveAPI(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	broken, action := p.broken, r.URL.Query().Get("action")
	list, listed := p.lists[action]
	series := p.series[r.URL.Query().Get("series_id")]
	p.mu.Unlock()
	switch {
	case broken:
		http.Error(w, "outage", http.StatusInternalServerError)
	case action == "":
		fmt.Fprint(w, `{"user_info":{"auth":1,"status":"Active","exp_date":"1900000000","active_cons":"0","max_connections":"3"}}`)
	case action == "get_series_info":
		fmt.Fprint(w, series)
	case action == "get_vod_info":
		fmt.Fprint(w, `{"info":{"plot":"a plot"},"movie_data":{}}`)
	case listed:
		fmt.Fprint(w, list)
	default:
		fmt.Fprint(w, `[]`)
	}
}

func parseRange(header string, size int64) (start, end int64, partial, valid bool) {
	spec, found := strings.CutPrefix(header, "bytes=")
	if !found {
		return 0, size - 1, false, true
	}
	first, last, _ := strings.Cut(spec, "-")
	if first == "" {
		suffix, _ := strconv.ParseInt(last, 10, 64)
		return max(size-suffix, 0), size - 1, true, true
	}
	start, _ = strconv.ParseInt(first, 10, 64)
	end = size - 1
	if last != "" {
		end, _ = strconv.ParseInt(last, 10, 64)
		end = min(end, size-1)
	}
	return start, end, true, start < size
}

func flush(w http.ResponseWriter) {
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func serveFile(w http.ResponseWriter, r *http.Request, content []byte, fault Fault) {
	size := int64(len(content))
	start, end, partial, valid := parseRange(r.Header.Get("Range"), size)
	if !valid {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	switch fault.Kind {
	case Status:
		w.WriteHeader(fault.Code)
		return
	case EmptyBody:
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		return
	}
	header := w.Header()
	header.Set("Content-Type", "video/mp4")
	header.Set("Content-Length", strconv.FormatInt(end-start+1, 10))
	status := http.StatusOK
	if partial {
		header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		status = http.StatusPartialContent
	}
	w.WriteHeader(status)
	stopAt := end + 1
	faulted := fault.Kind != 0 && fault.At >= start && fault.At <= end
	if faulted {
		stopAt = fault.At
	}
	for position := start; position < stopAt; {
		next := min(position+writeStep, stopAt)
		if _, err := w.Write(content[position:next]); err != nil {
			return
		}
		flush(w)
		position = next
	}
	if faulted {
		misbehave(w, r, content[stopAt:end+1], fault.Kind)
	}
}

func misbehave(w http.ResponseWriter, r *http.Request, rest []byte, kind FaultKind) {
	switch kind {
	case Stall:
		flush(w)
		<-r.Context().Done()
	case Cut:
		panic(http.ErrAbortHandler)
	case Trickle:
		for _, b := range rest {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(trickleEvery):
			}
			if _, err := w.Write([]byte{b}); err != nil {
				return
			}
			flush(w)
		}
	}
}

func serveLive(w http.ResponseWriter, r *http.Request, fault Fault) {
	switch fault.Kind {
	case Status:
		w.WriteHeader(fault.Code)
		return
	case EmptyBody:
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		return
	}
	block := make([]byte, livePacket*livePackets)
	for i := 0; i < len(block); i += livePacket {
		block[i] = tsSync
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)
	var sent int64
	for r.Context().Err() == nil {
		if fault.Kind != 0 && sent >= fault.At {
			misbehave(w, r, nil, fault.Kind)
			return
		}
		if _, err := w.Write(block); err != nil {
			return
		}
		flush(w)
		sent += int64(len(block))
		time.Sleep(liveEvery)
	}
}
