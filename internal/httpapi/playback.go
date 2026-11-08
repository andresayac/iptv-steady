package httpapi

import (
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"iptv-steady/internal/catalog"
	"iptv-steady/internal/relay"
	"iptv-steady/internal/xtream"
)

const (
	defaultLiveExtension = "ts"
	hlsExtension         = "m3u8"
	playlistBufferSize   = 64 << 10
)

func (s *Server) play(kind xtream.Kind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		user := s.authenticate(r.PathValue("user"), r.PathValue("password"))
		if user == nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		name, extension, _ := strings.Cut(r.PathValue("file"), ".")
		id, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		media, found := s.media(r.Context(), kind, id)
		if !found {
			http.NotFound(w, r)
			return
		}
		viewer := relay.Viewer{User: user.Username, Player: r.UserAgent()}
		switch {
		case kind != xtream.Live:
			s.engine.ServeFile(w, r, viewer, media)
		case extension == hlsExtension:
			s.redirectLive(w, r, media)
		default:
			s.engine.ServeLive(w, r, viewer, media, liveExtension(extension))
		}
	}
}

func liveExtension(requested string) string {
	if requested == "" {
		return defaultLiveExtension
	}
	return requested
}

func (s *Server) redirectLive(w http.ResponseWriter, r *http.Request, media relay.Media) {
	target, err := s.engine.LiveURL(media, hlsExtension)
	if err != nil {
		http.Error(w, "no working source", http.StatusBadGateway)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) media(ctx context.Context, kind xtream.Kind, id int64) (relay.Media, bool) {
	if kind == xtream.Series {
		return s.details.episodeMedia(ctx, id)
	}
	title := s.catalog.Find(kind, id)
	if title == nil {
		return relay.Media{}, false
	}
	return mediaOf(kind, title), true
}

func mediaOf(kind xtream.Kind, title *catalog.Title) relay.Media {
	sources := make([]relay.Source, 0, len(title.Copies))
	for _, version := range title.Copies {
		sources = append(sources, relay.Source{Account: version.Account, ID: version.ID, Extension: version.Extension})
	}
	return relay.Media{Kind: kind, TitleID: title.ID, Title: title.Name, Sources: sources}
}

func attribute(value string) string {
	return strings.NewReplacer(`"`, "'", "\n", " ", "\r", " ").Replace(value)
}

func (s *Server) handlePlaylist(w http.ResponseWriter, r *http.Request) {
	username, password := r.FormValue("username"), r.FormValue("password")
	if s.authenticate(username, password) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	current := s.catalog.Current()
	if current == nil {
		http.Error(w, "catalog not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="playlist.m3u"`)
	var out io.Writer = w
	if acceptsGzip(r) {
		w.Header().Set("Content-Encoding", "gzip")
		compressor, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		defer compressor.Close()
		out = compressor
	}
	buffered := bufio.NewWriterSize(out, playlistBufferSize)
	defer buffered.Flush()
	base := s.publicBase(r)
	credentials := url.PathEscape(username) + "/" + url.PathEscape(password)
	fmt.Fprintln(buffered, "#EXTM3U")
	writeEntries(buffered, current.Section(xtream.Live), base, credentials)
	writeEntries(buffered, current.Section(xtream.VOD), base, credentials)
}

func writeEntries(w io.Writer, section *catalog.Section, base, credentials string) {
	names := make(map[int64]string, len(section.Categories))
	for _, category := range section.Categories {
		names[category.ID] = category.Name
	}
	for _, title := range section.Titles {
		segment, extension := "movie", title.PreferredExtension()
		if section.Kind == xtream.Live {
			segment, extension = "live", defaultLiveExtension
		}
		fmt.Fprintf(w, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-logo=\"%s\" group-title=\"%s\",%s\n", attribute(title.EPG), attribute(title.Name), attribute(title.Icon), attribute(names[title.Categories[0]]), attribute(title.Name))
		fmt.Fprintf(w, "%s/%s/%s/%d.%s\n", base, segment, credentials, title.ID, extension)
	}
}

func (s *Server) handleGuide(w http.ResponseWriter, r *http.Request) {
	if s.authenticate(r.FormValue("username"), r.FormValue("password")) == nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	for _, account := range s.pool.All() {
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, account.Client.GuideURL(), nil)
		if err != nil {
			continue
		}
		request.Header.Set("User-Agent", s.pool.UserAgent())
		response, err := s.pool.APIClient().Do(request)
		if err != nil {
			slog.Warn("guide unavailable", "error", account.Client.Sanitize(err))
			continue
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			continue
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		_, _ = io.Copy(w, response.Body)
		response.Body.Close()
		return
	}
	http.Error(w, "guide unavailable", http.StatusBadGateway)
}
