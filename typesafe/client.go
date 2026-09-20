// Package typesafe is a small Go client for the TypeSafe System One HTTP API.
//
// There is no official Go SDK, so this package mirrors the shape of the
// JavaScript SDK: a client with SystemOne and ListModels, question constructors,
// typed answers, retry with backoff, and structured errors.
// API reference: https://docs.typesafe.ai/api
package typesafe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	DefaultBaseURL = "https://api.typesafe.ai"
	DefaultModel   = "jev-latest"
)

// Client calls the TypeSafe API. Construct it with New.
type Client struct {
	apiKey         string
	baseURL        string
	model          string
	http           *http.Client
	maxRetries     int
	backoffInitial time.Duration
	backoffMax     time.Duration

	attempts      atomic.Uint64
	retries       atomic.Uint64
	connErrs      atomic.Uint64
	retryAfterMax atomic.Int64 // milliseconds
	mu            sync.Mutex
	statuses      map[int]uint64
}

// Stats are cumulative counters for one client, useful for load tests.
type Stats struct {
	Attempts uint64         // HTTP attempts made, including retries
	Retries  uint64         // attempts beyond the first for a request
	ConnErrs uint64         // transport or body-read failures
	Statuses map[int]uint64 // count of each HTTP status seen
	// RetryAfterMax is the largest Retry-After the server has sent.
	RetryAfterMax time.Duration
}

// Stats returns a snapshot of the client's counters.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := Stats{Attempts: c.attempts.Load(), Retries: c.retries.Load(), ConnErrs: c.connErrs.Load(),
		RetryAfterMax: time.Duration(c.retryAfterMax.Load()) * time.Millisecond, Statuses: map[int]uint64{}}
	for k, v := range c.statuses {
		st.Statuses[k] = v
	}
	return st
}

// Option configures a Client.
type Option func(*Client)

func WithAPIKey(key string) Option { return func(c *Client) { c.apiKey = key } }
func WithBaseURL(url string) Option {
	return func(c *Client) { c.baseURL = strings.TrimRight(url, "/") }
}
func WithModel(model string) Option        { return func(c *Client) { c.model = model } }
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }
func WithMaxRetries(n int) Option          { return func(c *Client) { c.maxRetries = n } }
func WithTimeout(d time.Duration) Option   { return func(c *Client) { c.http.Timeout = d } }

// New builds a client. Explicit options win over the environment variables
// TYPESAFE_API_KEY, TYPESAFE_BASE_URL and TYPESAFE_DEFAULT_MODEL.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		apiKey:         os.Getenv("TYPESAFE_API_KEY"),
		baseURL:        envOr("TYPESAFE_BASE_URL", DefaultBaseURL),
		model:          envOr("TYPESAFE_DEFAULT_MODEL", DefaultModel),
		http:           &http.Client{Timeout: 10 * time.Second},
		maxRetries:     2,
		backoffInitial: 500 * time.Millisecond,
		backoffMax:     5 * time.Second,
		statuses:       map[int]uint64{},
	}
	for _, o := range opts {
		o(c)
	}
	if c.apiKey == "" {
		return nil, errors.New("typesafe: no API key; set TYPESAFE_API_KEY or pass WithAPIKey")
	}
	return c, nil
}

// Request is the body of POST /v1/systemone.
type Request struct {
	// State is text, a JSON-marshalable object or slice, or nil.
	State any `json:"state"`
	// Questions must contain at least one entry. The server rejects an empty map with 422.
	Questions Questions `json:"questions"`
	// Model overrides the client default when non-empty.
	Model string `json:"model,omitempty"`
}

// Usage is the token accounting for one request. Only input tokens are billed.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Result is a successful System One response.
type Result struct {
	Model     string  `json:"model"`
	Answers   Answers `json:"answers"`
	Usage     Usage   `json:"usage"`
	RequestID string  `json:"-"`
}

// SystemOne evaluates every question in req against req.State in one call.
func (c *Client) SystemOne(ctx context.Context, req Request) (*Result, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	var out Result
	rid, err := c.do(ctx, http.MethodPost, "/v1/systemone", req, &out)
	if err != nil {
		return nil, err
	}
	out.RequestID = rid
	return &out, nil
}

// ModelCard describes a model name the account may send in Request.Model.
type ModelCard struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	ReleaseDate string `json:"release_date"`
}

// ListModels returns GET /v1/models.
func (c *Client) ListModels(ctx context.Context) ([]ModelCard, error) {
	var out struct {
		Models []ModelCard `json:"models"`
	}
	_, err := c.do(ctx, http.MethodGet, "/v1/models", nil, &out)
	return out.Models, err
}

// APIError is a non-2xx response.
type APIError struct {
	Status    int
	RequestID string
	// Type is the server's error_type (for example "authentication_error"),
	// or "validation_error" for a 422 detail list.
	Type    string
	Message string
	Body    json.RawMessage
	// RetryAfter is the server's requested delay, when it sent one.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("typesafe: http %d %s: %s", e.Status, e.Type, e.Message)
}

// Retryable reports whether the request may succeed on retry.
func (e *APIError) Retryable() bool {
	return e.Status == http.StatusRequestTimeout || e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// ConnectionError wraps a transport failure or a body read failure.
type ConnectionError struct{ Err error }

func (e *ConnectionError) Error() string { return "typesafe: connection: " + e.Err.Error() }
func (e *ConnectionError) Unwrap() error { return e.Err }

func (c *Client) do(ctx context.Context, method, path string, body, out any) (string, error) {
	var payload []byte
	if body != nil {
		var err error
		if payload, err = json.Marshal(body); err != nil {
			return "", fmt.Errorf("typesafe: encode request: %w", err)
		}
	}
	for attempt := 0; ; attempt++ {
		rid, retryAfter, err := c.once(ctx, method, path, payload, out)
		if err == nil {
			return rid, nil
		}
		if attempt >= c.maxRetries || ctx.Err() != nil || !retryable(err) {
			return rid, err
		}
		select {
		case <-ctx.Done():
			return rid, ctx.Err()
		case <-time.After(c.backoff(attempt, retryAfter)):
			c.retries.Add(1)
		}
	}
}

func (c *Client) once(ctx context.Context, method, path string, payload []byte, out any) (rid string, retryAfter time.Duration, err error) {
	var rdr io.Reader
	if payload != nil {
		rdr = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.attempts.Add(1)
	resp, err := c.http.Do(req)
	if err != nil {
		c.connErrs.Add(1)
		return "", 0, &ConnectionError{Err: err}
	}
	defer resp.Body.Close()
	c.mu.Lock()
	c.statuses[resp.StatusCode]++
	c.mu.Unlock()
	rid = resp.Header.Get("x-typesafe-request-id")
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		c.connErrs.Add(1)
		return rid, 0, &ConnectionError{Err: err}
	}
	if resp.StatusCode >= 400 {
		ae := newAPIError(resp.StatusCode, rid, data)
		ae.RetryAfter = parseRetryAfter(resp.Header)
		if ms := ae.RetryAfter.Milliseconds(); ms > c.retryAfterMax.Load() {
			c.retryAfterMax.Store(ms)
		}
		return rid, ae.RetryAfter, ae
	}
	if err := json.Unmarshal(data, out); err != nil {
		return rid, 0, fmt.Errorf("typesafe: decode response: %w", err)
	}
	return rid, 0, nil
}

func newAPIError(status int, rid string, data []byte) *APIError {
	e := &APIError{Status: status, RequestID: rid, Body: data}
	var env struct {
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(data, &env) == nil && len(env.Detail) > 0 {
		// {"detail":{"error_type":"...","message":"..."}}
		var d struct {
			ErrorType string `json:"error_type"`
			Message   string `json:"message"`
		}
		if json.Unmarshal(env.Detail, &d) == nil && (d.Message != "" || d.ErrorType != "") {
			e.Type, e.Message = d.ErrorType, d.Message
			if e.Message == "" {
				e.Message = http.StatusText(status)
			}
			return e
		}
		// {"detail":[{"type":"missing","loc":["body","model"],"msg":"Field required"}, ...]}
		var list []struct {
			Loc []any  `json:"loc"`
			Msg string `json:"msg"`
		}
		if json.Unmarshal(env.Detail, &list) == nil && len(list) > 0 {
			parts := make([]string, 0, len(list))
			for _, it := range list {
				locs := make([]string, 0, len(it.Loc))
				for _, l := range it.Loc {
					locs = append(locs, fmt.Sprint(l))
				}
				parts = append(parts, strings.Join(locs, ".")+": "+it.Msg)
			}
			e.Type, e.Message = "validation_error", strings.Join(parts, "; ")
			return e
		}
	}
	e.Type = "http_error"
	e.Message = http.StatusText(status)
	if body := strings.TrimSpace(string(data)); body != "" && len(body) <= 400 {
		e.Message += ": " + body
	}
	return e
}

func retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Retryable()
	}
	var ce *ConnectionError
	return errors.As(err, &ce)
}

func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 && retryAfter <= time.Minute {
		return retryAfter
	}
	d := c.backoffInitial << attempt
	if d > c.backoffMax {
		d = c.backoffMax
	}
	jitter := time.Duration(rand.Float64() * 0.25 * float64(d))
	return d - jitter
}

func parseRetryAfter(h http.Header) time.Duration {
	if ms := h.Get("retry-after-ms"); ms != "" {
		if n, err := strconv.Atoi(ms); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	if s := h.Get("Retry-After"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return time.Duration(n) * time.Second
		}
	}
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
