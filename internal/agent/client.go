package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// maxResponseBytes bounds how much of a controller response the agent reads.
const maxResponseBytes = 4 << 20

// APIError is a non-2xx response from the controller.
type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("controller returned %d %s: %s", e.Status, e.Code, e.Message)
}

// Retryable reports whether the same request may succeed later.
func (e *APIError) Retryable() bool {
	return e.Status >= 500 || e.Status == http.StatusTooManyRequests || e.Status == http.StatusRequestTimeout
}

// IsUnauthorized reports whether err means the agent's credential was
// rejected (revoked, or the agent was deleted).
func IsUnauthorized(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusForbidden)
}

// IsPermanent reports whether retrying the same request cannot help.
func IsPermanent(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && !ae.Retryable()
}

// Client talks to the controller's agent API.
type Client struct {
	base      *url.URL
	http      *http.Client
	token     string
	userAgent string
}

// NewClient validates baseURL and returns a client. Plain http is accepted
// only for loopback addresses, so credentials never cross a network in clear
// text by accident.
func NewClient(baseURL string, hc *http.Client, userAgent string) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid controller URL %q", baseURL)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" && !strings.HasSuffix(h, ".localhost") {
			return nil, fmt.Errorf("controller URL must use https (http is allowed only for localhost)")
		}
	default:
		return nil, fmt.Errorf("controller URL must use https")
	}
	return &Client{base: u, http: hc, userAgent: userAgent}, nil
}

// WithToken returns a copy of the client that authenticates as an agent.
func (c *Client) WithToken(token string) *Client {
	cp := *c
	cp.token = token
	return &cp
}

// Enroll exchanges an enrolment token for agent credentials.
func (c *Client) Enroll(ctx context.Context, req wire.EnrollRequest) (wire.EnrollResponse, error) {
	var out wire.EnrollResponse
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	resp, err := c.do(ctx, http.MethodPost, "/api/v1/agent/enroll", bytes.NewReader(body), nil)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	return out, decode(resp, &out)
}

// Plan fetches the agent's test plan. When etag matches the controller's
// current plan, notModified is true and plan is empty.
func (c *Client) Plan(ctx context.Context, etag string) (plan wire.Plan, newETag string, notModified bool, err error) {
	h := http.Header{}
	if etag != "" {
		h.Set("If-None-Match", etag)
	}
	resp, err := c.do(ctx, http.MethodGet, "/api/v1/agent/plan", nil, h)
	if err != nil {
		return plan, "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return plan, etag, true, nil
	}
	if err := decode(resp, &plan); err != nil {
		return plan, "", false, err
	}
	return plan, resp.Header.Get("ETag"), false, nil
}

// Upload sends a batch of results, gzip-compressed to save metered bandwidth.
func (c *Client) Upload(ctx context.Context, results []wire.Result) (wire.IngestResponse, error) {
	var out wire.IngestResponse
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if err := json.NewEncoder(zw).Encode(wire.ResultBatch{Results: results}); err != nil {
		return out, err
	}
	if err := zw.Close(); err != nil {
		return out, err
	}
	h := http.Header{"Content-Encoding": {"gzip"}}
	resp, err := c.do(ctx, http.MethodPost, "/api/v1/agent/results", &buf, h)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	return out, decode(resp, &out)
}

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, h http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, body)
	if err != nil {
		return nil, err
	}
	for k, v := range h {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	return resp, nil
}

func decode(resp *http.Response, out any) error {
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var eb wire.ErrorBody
		if json.Unmarshal(data, &eb) != nil || eb.Error.Code == "" {
			eb.Error = wire.ErrorDetail{Code: "http_error", Message: http.StatusText(resp.StatusCode)}
		}
		return &APIError{Status: resp.StatusCode, Code: eb.Error.Code, Message: eb.Error.Message}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	return nil
}
