// Package client is a thin Go client for the Chroma v2 HTTP API, as served
// by Kaleid and by Chroma itself.
//
//	c := client.New("http://localhost:8000")
//	col, err := c.CreateCollection(ctx, "docs", &client.CreateCollectionOptions{GetOrCreate: true})
//	err = col.Add(ctx, client.Records{
//		IDs:        []string{"a", "b"},
//		Embeddings: [][]float32{{1, 0}, {0, 1}},
//		Documents:  []string{"first", "second"},
//	})
//	res, err := col.Query(ctx, client.QueryOptions{Embeddings: [][]float32{{1, 0}}, NResults: 1})
//
// The client does not compute embeddings: pass vectors produced by your own
// embedding model.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Default tenant and database names.
const (
	DefaultTenant   = "default_tenant"
	DefaultDatabase = "default_database"
)

// Client talks to one server, scoped to a tenant and database. It is safe for
// concurrent use.
type Client struct {
	base     string
	http     *http.Client
	headers  http.Header
	tenant   string
	database string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the underlying HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithToken authenticates with "Authorization: Bearer <token>".
func WithToken(token string) Option {
	return func(c *Client) { c.headers.Set("Authorization", "Bearer "+token) }
}

// WithChromaToken authenticates with the "X-Chroma-Token" header.
func WithChromaToken(token string) Option {
	return func(c *Client) { c.headers.Set("X-Chroma-Token", token) }
}

// WithHeader adds a header to every request.
func WithHeader(key, value string) Option { return func(c *Client) { c.headers.Add(key, value) } }

// WithTenant sets the tenant (default "default_tenant").
func WithTenant(tenant string) Option { return func(c *Client) { c.tenant = tenant } }

// WithDatabase sets the database (default "default_database").
func WithDatabase(database string) Option { return func(c *Client) { c.database = database } }

// New creates a client for baseURL, e.g. "http://localhost:8000".
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		base:     strings.TrimRight(baseURL, "/"),
		http:     &http.Client{Timeout: 60 * time.Second},
		headers:  http.Header{},
		tenant:   DefaultTenant,
		database: DefaultDatabase,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Scoped returns a copy of the client bound to another tenant and database.
func (c *Client) Scoped(tenant, database string) *Client {
	cp := *c
	cp.tenant, cp.database = tenant, database
	return &cp
}

// Tenant returns the client's tenant.
func (c *Client) Tenant() string { return c.tenant }

// Database returns the client's database.
func (c *Client) Database() string { return c.database }

// Error is an error response from the server.
type Error struct {
	StatusCode int
	// Name is the server's error class, e.g. "NotFoundError".
	Name    string
	Message string
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("chroma: HTTP %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("chroma: %s (HTTP %d): %s", e.Name, e.StatusCode, e.Message)
}

func statusIs(err error, code int) bool {
	var e *Error
	return errors.As(err, &e) && e.StatusCode == code
}

// IsNotFound reports whether err is a 404 from the server.
func IsNotFound(err error) bool { return statusIs(err, http.StatusNotFound) }

// IsConflict reports whether err is a 409 (e.g. the resource already exists).
func IsConflict(err error) bool { return statusIs(err, http.StatusConflict) }

// IsInvalidArgument reports whether err is a 400 from the server.
func IsInvalidArgument(err error) bool { return statusIs(err, http.StatusBadRequest) }

func esc(s string) string { return url.PathEscape(s) }

func (c *Client) dbPath() string {
	return "/api/v2/tenants/" + esc(c.tenant) + "/databases/" + esc(c.database)
}

// do sends a request and decodes a JSON response into out (if non-nil).
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return err
	}
	for k, vs := range c.headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		e := &Error{StatusCode: resp.StatusCode}
		var payload struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &payload) == nil && (payload.Error != "" || payload.Message != "") {
			e.Name, e.Message = payload.Error, payload.Message
		} else {
			e.Message = strings.TrimSpace(string(raw))
			if e.Message == "" {
				e.Message = http.StatusText(resp.StatusCode)
			}
		}
		return e
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(out)
}

// ---------------------------------------------------------------------------
// System
// ---------------------------------------------------------------------------

// Heartbeat returns the server's clock in nanoseconds.
func (c *Client) Heartbeat(ctx context.Context) (int64, error) {
	var out map[string]json.Number
	if err := c.do(ctx, http.MethodGet, "/api/v2/heartbeat", nil, &out); err != nil {
		return 0, err
	}
	return out["nanosecond heartbeat"].Int64()
}

// Version returns the server's API version string.
func (c *Client) Version(ctx context.Context) (string, error) {
	var v string
	err := c.do(ctx, http.MethodGet, "/api/v2/version", nil, &v)
	return v, err
}

// Identity is the authenticated caller.
type Identity struct {
	UserID    string   `json:"user_id"`
	Tenant    string   `json:"tenant"`
	Databases []string `json:"databases"`
}

// Identity returns the authenticated caller's identity.
func (c *Client) Identity(ctx context.Context) (*Identity, error) {
	var id Identity
	if err := c.do(ctx, http.MethodGet, "/api/v2/auth/identity", nil, &id); err != nil {
		return nil, err
	}
	return &id, nil
}

// Reset deletes all data. The server must allow resets.
func (c *Client) Reset(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/v2/reset", nil, nil)
}

// ---------------------------------------------------------------------------
// Tenants and databases
// ---------------------------------------------------------------------------

// CreateTenant creates a tenant.
func (c *Client) CreateTenant(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/api/v2/tenants", map[string]string{"name": name}, nil)
}

// Tenant is a tenant.
type Tenant struct {
	Name         string  `json:"name"`
	ResourceName *string `json:"resource_name"`
}

// GetTenant fetches a tenant.
func (c *Client) GetTenant(ctx context.Context, name string) (*Tenant, error) {
	var t Tenant
	if err := c.do(ctx, http.MethodGet, "/api/v2/tenants/"+esc(name), nil, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Database is a database within a tenant.
type Database struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Tenant string `json:"tenant"`
}

// CreateDatabase creates a database in the client's tenant.
func (c *Client) CreateDatabase(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodPost, "/api/v2/tenants/"+esc(c.tenant)+"/databases", map[string]string{"name": name}, nil)
}

// GetDatabase fetches a database in the client's tenant.
func (c *Client) GetDatabase(ctx context.Context, name string) (*Database, error) {
	var d Database
	if err := c.do(ctx, http.MethodGet, "/api/v2/tenants/"+esc(c.tenant)+"/databases/"+esc(name), nil, &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// ListDatabases lists databases in the client's tenant. limit <= 0 means no limit.
func (c *Client) ListDatabases(ctx context.Context, limit, offset int) ([]Database, error) {
	var out []Database
	err := c.do(ctx, http.MethodGet, "/api/v2/tenants/"+esc(c.tenant)+"/databases"+paging(limit, offset), nil, &out)
	return out, err
}

// DeleteDatabase deletes a database and all of its collections.
func (c *Client) DeleteDatabase(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/v2/tenants/"+esc(c.tenant)+"/databases/"+esc(name), nil, nil)
}

func paging(limit, offset int) string {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
