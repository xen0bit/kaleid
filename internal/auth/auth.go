// Package auth authenticates requests and authorizes tenant/database access.
//
// Two providers exist: "none" (the default, matching open-source Chroma,
// which performs no server-side auth) and "token", which maps static bearer
// tokens to a tenant and a set of databases. Tokens are read from the
// "Authorization: Bearer <token>" header or the "X-Chroma-Token" header,
// the two transports Chroma's clients support.
package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/xen0bit/kaleid/internal/apierr"
)

// Wildcard grants access to every tenant or database.
const Wildcard = "*"

// Identity is the authenticated principal.
type Identity struct {
	UserID    string   `json:"user_id"`
	Tenant    string   `json:"tenant"`
	Databases []string `json:"databases"`
}

// CanAccess reports whether the identity may access tenant/database. An
// empty database checks tenant-level access only.
func (id *Identity) CanAccess(tenant, database string) bool {
	if id.Tenant != Wildcard && id.Tenant != tenant {
		return false
	}
	if database == "" {
		return true
	}
	for _, d := range id.Databases {
		if d == Wildcard || d == database {
			return true
		}
	}
	return false
}

// Provider authenticates requests.
type Provider interface {
	Authenticate(r *http.Request) (*Identity, error)
}

// None performs no authentication and grants full access. Its identity
// mirrors Chroma's default identity response.
type None struct{}

// Authenticate implements Provider.
func (None) Authenticate(*http.Request) (*Identity, error) {
	return &Identity{UserID: "", Tenant: Wildcard, Databases: []string{Wildcard}}, nil
}

// TokenEntry configures one static token.
type TokenEntry struct {
	Token     string   `json:"token"`
	UserID    string   `json:"user_id"`
	Tenant    string   `json:"tenant"`
	Databases []string `json:"databases"`
}

// Token authenticates static bearer tokens.
type Token struct {
	entries []tokenEntry
}

type tokenEntry struct {
	hash [32]byte
	id   Identity
}

// NewToken builds a token provider. Entries without a tenant get full access.
func NewToken(entries []TokenEntry) (*Token, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("token auth enabled but no tokens configured")
	}
	t := &Token{}
	for i, e := range entries {
		if e.Token == "" {
			return nil, fmt.Errorf("token entry %d has an empty token", i)
		}
		id := Identity{UserID: e.UserID, Tenant: e.Tenant, Databases: e.Databases}
		if id.Tenant == "" {
			id.Tenant = Wildcard
		}
		if len(id.Databases) == 0 {
			id.Databases = []string{Wildcard}
		}
		if id.UserID == "" {
			id.UserID = fmt.Sprintf("token-%d", i)
		}
		t.entries = append(t.entries, tokenEntry{hash: sha256.Sum256([]byte(e.Token)), id: id})
	}
	return t, nil
}

// LoadTokenFile reads a JSON array of TokenEntry.
func LoadTokenFile(path string) ([]TokenEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []TokenEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return entries, nil
}

// ExtractToken reads the credential from the supported headers.
func ExtractToken(r *http.Request) string {
	if v := r.Header.Get("X-Chroma-Token"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("Authorization"); v != "" {
		if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
			return strings.TrimSpace(v[7:])
		}
	}
	return ""
}

// Authenticate implements Provider.
func (t *Token) Authenticate(r *http.Request) (*Identity, error) {
	tok := ExtractToken(r)
	if tok == "" {
		return nil, apierr.Unauthorized("Unauthorized")
	}
	h := sha256.Sum256([]byte(tok))
	var found *Identity
	for i := range t.entries {
		// Constant-time comparison over every entry.
		if subtle.ConstantTimeCompare(h[:], t.entries[i].hash[:]) == 1 {
			id := t.entries[i].id
			found = &id
		}
	}
	if found == nil {
		return nil, apierr.Unauthorized("Unauthorized")
	}
	return found, nil
}
