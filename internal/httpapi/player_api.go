package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"iptv-steady/internal/config"
	"iptv-steady/internal/xtream"
)

func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	user := s.authenticate(r.FormValue("username"), r.FormValue("password"))
	if user == nil {
		writeJSON(w, map[string]any{"user_info": map[string]any{"auth": 0}})
		return
	}
	action := r.FormValue("action")
	switch action {
	case "":
		s.writeLogin(w, r, user)
	case "get_vod_categories":
		s.writeCategories(w, r, xtream.VOD)
	case "get_series_categories":
		s.writeCategories(w, r, xtream.Series)
	case "get_live_categories":
		s.writeCategories(w, r, xtream.Live)
	case "get_vod_streams":
		s.writeStreams(w, r, xtream.VOD)
	case "get_series":
		s.writeStreams(w, r, xtream.Series)
	case "get_live_streams":
		s.writeStreams(w, r, xtream.Live)
	case "get_vod_info":
		s.handleMovieInfo(w, r)
	case "get_series_info":
		s.handleSeriesInfo(w, r)
	case "get_short_epg", "get_simple_data_table":
		s.handleEPG(w, r, action)
	default:
		writeJSON(w, []any{})
	}
}

func (s *Server) latestExpiry() string {
	var latest int64
	for _, account := range s.pool.All() {
		if health := account.Health(); health != nil && health.Status.ExpiresAt > latest {
			latest = health.Status.ExpiresAt
		}
	}
	if latest == 0 {
		return "null"
	}
	return strconv.FormatInt(latest, 10)
}

func (s *Server) writeLogin(w http.ResponseWriter, r *http.Request, user *config.User) {
	base, _ := url.Parse(s.publicBase(r))
	port, httpsPort := publicPort(base), ""
	if base.Scheme == "https" {
		httpsPort = port
	}
	now := time.Now().UTC()
	writeJSON(w, map[string]any{
		"user_info": map[string]any{
			"username": user.Username, "password": user.Password, "message": "", "auth": 1, "status": "Active", "exp_date": s.latestExpiry(),
			"is_trial": "0", "active_cons": "0", "created_at": "0", "max_connections": strconv.Itoa(user.MaxConnections), "allowed_output_formats": []string{"ts"},
		},
		"server_info": map[string]any{
			"url": base.Hostname(), "port": port, "https_port": httpsPort, "server_protocol": base.Scheme, "rtmp_port": "", "timezone": "UTC",
			"timestamp_now": now.Unix(), "time_now": now.Format(time.DateTime),
		},
	})
}

func (s *Server) writeCategories(w http.ResponseWriter, r *http.Request, kind xtream.Kind) {
	section := s.section(kind)
	if section == nil {
		writeJSON(w, []any{})
		return
	}
	writePayload(w, r, section.CategoryList())
}

func (s *Server) writeStreams(w http.ResponseWriter, r *http.Request, kind xtream.Kind) {
	section := s.section(kind)
	if section == nil {
		writeJSON(w, []any{})
		return
	}
	if category := r.FormValue("category_id"); category != "" {
		writePayload(w, r, section.ListCategory(parseID(category)))
		return
	}
	writePayload(w, r, section.List())
}

func (s *Server) handleMovieInfo(w http.ResponseWriter, r *http.Request) {
	title := s.catalog.Find(xtream.VOD, parseID(r.FormValue("vod_id")))
	if title == nil {
		writeJSON(w, map[string]any{"info": []any{}, "movie_data": []any{}})
		return
	}
	writeRawJSON(w, s.details.movie(r.Context(), title))
}

func (s *Server) handleSeriesInfo(w http.ResponseWriter, r *http.Request) {
	empty := map[string]any{"seasons": []any{}, "info": []any{}, "episodes": []any{}}
	title := s.catalog.Find(xtream.Series, parseID(r.FormValue("series_id")))
	if title == nil {
		writeJSON(w, empty)
		return
	}
	entry, err := s.details.series(r.Context(), title)
	if err != nil {
		slog.Warn("series details unavailable", "title", title.Name, "error", err)
		writeJSON(w, empty)
		return
	}
	writeRawJSON(w, entry.body)
}

func (s *Server) handleEPG(w http.ResponseWriter, r *http.Request, action string) {
	if title := s.catalog.Find(xtream.Live, parseID(r.FormValue("stream_id"))); title != nil {
		for _, version := range title.Copies {
			params := url.Values{"stream_id": {version.ID}}
			if limit := r.FormValue("limit"); limit != "" {
				params.Set("limit", limit)
			}
			var answer json.RawMessage
			ctx, cancel := context.WithTimeout(r.Context(), s.cfg.Details.VODTimeout)
			err := s.pool.Get(version.Account).Client.Get(ctx, action, params, &answer)
			cancel()
			if err == nil {
				writeRawJSON(w, answer)
				return
			}
			slog.Debug("guide lookup failed", "error", err)
		}
	}
	writeJSON(w, map[string]any{"epg_listings": []any{}})
}
