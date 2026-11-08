package xtream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Credentials struct {
	Name     string
	BaseURL  string
	Username string
	Password string
}

type Client struct {
	credentials Credentials
	userAgent   string
	httpClient  *http.Client
	secrets     *strings.Replacer
}

func NewClient(credentials Credentials, userAgent string, httpClient *http.Client) *Client {
	return &Client{
		credentials: credentials,
		userAgent:   userAgent,
		httpClient:  httpClient,
		secrets:     strings.NewReplacer(credentials.Password, "***", credentials.Username, "***"),
	}
}

// Sanitize removes the URL and the credentials from an error, because net/http errors quote the full request.
func (c *Client) Sanitize(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("%s: %s", c.credentials.Name, c.secrets.Replace(err.Error()))
}

func (c *Client) apiURL(action string, params url.Values) string {
	query := url.Values{"username": {c.credentials.Username}, "password": {c.credentials.Password}}
	if action != "" {
		query.Set("action", action)
	}
	for key, values := range params {
		query[key] = values
	}
	return c.credentials.BaseURL + "/player_api.php?" + query.Encode()
}

func (c *Client) Get(ctx context.Context, action string, params url.Values, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiURL(action, params), nil)
	if err != nil {
		return c.Sanitize(err)
	}
	request.Header.Set("User-Agent", c.userAgent)
	response, err := c.httpClient.Do(request)
	if err != nil {
		return c.Sanitize(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return c.Sanitize(fmt.Errorf("%s: HTTP %d", actionName(action), response.StatusCode))
	}
	if err := json.NewDecoder(response.Body).Decode(out); err != nil {
		return c.Sanitize(fmt.Errorf("%s: %w", actionName(action), err))
	}
	return nil
}

func actionName(action string) string {
	if action == "" {
		return "login"
	}
	return action
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var login struct {
		UserInfo struct {
			Auth           Text `json:"auth"`
			Status         Text `json:"status"`
			ExpDate        Text `json:"exp_date"`
			ActiveCons     Text `json:"active_cons"`
			MaxConnections Text `json:"max_connections"`
		} `json:"user_info"`
	}
	if err := c.Get(ctx, "", nil, &login); err != nil {
		return Status{}, err
	}
	info := login.UserInfo
	expires, _ := strconv.ParseInt(string(info.ExpDate), 10, 64)
	active, _ := strconv.Atoi(string(info.ActiveCons))
	limit, _ := strconv.Atoi(string(info.MaxConnections))
	return Status{Authorized: info.Auth == "1", State: string(info.Status), ExpiresAt: expires, Active: active, Max: limit}, nil
}

var catalogActions = map[Kind][2]string{
	VOD:    {"get_vod_categories", "get_vod_streams"},
	Series: {"get_series_categories", "get_series"},
	Live:   {"get_live_categories", "get_live_streams"},
}

func (c *Client) FetchCatalog(ctx context.Context) (*Catalog, error) {
	catalog := &Catalog{FetchedAt: time.Now().UTC(), Categories: map[Kind][]Category{}, Streams: map[Kind][]Stream{}}
	for _, kind := range Kinds {
		actions := catalogActions[kind]
		var categories []Category
		if err := c.Get(ctx, actions[0], nil, &categories); err != nil {
			return nil, err
		}
		var streams []Stream
		if err := c.Get(ctx, actions[1], nil, &streams); err != nil {
			return nil, err
		}
		catalog.Categories[kind], catalog.Streams[kind] = categories, streams
	}
	return catalog, nil
}

var streamSegments = map[Kind]string{VOD: "movie", Series: "series", Live: "live"}

func (c *Client) StreamURL(kind Kind, id, extension string) string {
	address := fmt.Sprintf("%s/%s/%s/%s/%s", c.credentials.BaseURL, streamSegments[kind], url.PathEscape(c.credentials.Username), url.PathEscape(c.credentials.Password), id)
	if extension != "" {
		address += "." + extension
	}
	return address
}

func (c *Client) GuideURL() string {
	query := url.Values{"username": {c.credentials.Username}, "password": {c.credentials.Password}}
	return c.credentials.BaseURL + "/xmltv.php?" + query.Encode()
}
