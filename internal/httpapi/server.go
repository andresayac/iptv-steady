package httpapi

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"iptv-steady/internal/accounts"
	"iptv-steady/internal/catalog"
	"iptv-steady/internal/config"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/xtream"
)

type Server struct {
	cfg     *config.Config
	catalog *catalog.Manager
	pool    *accounts.Pool
	engine  *relay.Engine
	details *Details
}

func New(cfg *config.Config, manager *catalog.Manager, pool *accounts.Pool, engine *relay.Engine) *Server {
	return &Server{cfg: cfg, catalog: manager, pool: pool, engine: engine, details: newDetails(cfg.Details, manager, pool)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/player_api.php", s.handleAPI)
	mux.HandleFunc("/panel_api.php", s.handleAPI)
	mux.HandleFunc("/get.php", s.handlePlaylist)
	mux.HandleFunc("/xmltv.php", s.handleGuide)
	mux.HandleFunc("/movie/{user}/{password}/{file}", s.play(xtream.VOD))
	mux.HandleFunc("/series/{user}/{password}/{file}", s.play(xtream.Series))
	mux.HandleFunc("/live/{user}/{password}/{file}", s.play(xtream.Live))
	mux.HandleFunc("/{user}/{password}/{file}", s.play(xtream.Live))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	return allowBrowsers(mux)
}

// allowBrowsers lets a player that runs in a web page use the proxy. Such a player reads the
// lists and the files itself, from a page served from another address, and the browser hands
// it the answer only when the answer says any page may read it. A film is read in pieces with
// Range, and the player needs to see where each piece sits (Content-Range) and how long the
// file is. Credentials travel in the address, never in cookies, so allowing every origin gives
// a page nothing it could not already ask for.
func allowBrowsers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Access-Control-Allow-Origin", "*")
		header.Set("Access-Control-Expose-Headers", "Accept-Ranges, Content-Length, Content-Range, Content-Type, ETag")
		// The browser's question before a request it may not send unasked: a Range with more
		// than one piece, or a player that names itself (Firefox lets a page set User-Agent)
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			header.Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
			header.Set("Access-Control-Allow-Headers", "Range, If-None-Match, Accept-Encoding, User-Agent")
			header.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Server.Listen)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              s.cfg.Server.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: s.cfg.Server.ReadHeaderTimeout,
		IdleTimeout:       s.cfg.Server.IdleTimeout,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), s.cfg.Server.ShutdownTimeout)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	slog.Info("listening", "address", listener.Addr())
	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) authenticate(username, password string) *config.User {
	var found *config.User
	for i := range s.cfg.Users {
		user := &s.cfg.Users[i]
		match := subtle.ConstantTimeCompare([]byte(user.Username), []byte(username)) & subtle.ConstantTimeCompare([]byte(user.Password), []byte(password))
		if match == 1 {
			found = user
		}
	}
	return found
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(value)
}

func writeRawJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func acceptsGzip(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept-Encoding"), "gzip")
}

func writePayload(w http.ResponseWriter, r *http.Request, payload catalog.Payload) {
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("ETag", payload.ETag)
	header.Set("Vary", "Accept-Encoding")
	if r.Header.Get("If-None-Match") == payload.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if acceptsGzip(r) {
		header.Set("Content-Encoding", "gzip")
		header.Set("Content-Length", strconv.Itoa(len(payload.Compressed)))
		_, _ = w.Write(payload.Compressed)
		return
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload.Compressed))
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	_, _ = io.Copy(w, reader)
}

func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.Server.PublicURL != "" {
		return strings.TrimRight(s.cfg.Server.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) section(kind xtream.Kind) *catalog.Section {
	if current := s.catalog.Current(); current != nil {
		return current.Section(kind)
	}
	return nil
}

func parseID(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}

func publicPort(base *url.URL) string {
	switch {
	case base.Port() != "":
		return base.Port()
	case base.Scheme == "https":
		return "443"
	}
	return "80"
}
