// Package api implements the Chroma v2 HTTP API on top of the store.
package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/auth"
	"github.com/xen0bit/kaleid/internal/store"
)

// Config configures the API server.
type Config struct {
	AllowReset      bool
	MaxBatchSize    int
	MaxPayloadBytes int64
	CORSOrigins     []string
	Version         string
}

// Server serves the API.
type Server struct {
	store *store.Store
	auth  auth.Provider
	cfg   Config
	log   *slog.Logger
}

// New builds a server.
func New(st *store.Store, provider auth.Provider, cfg Config, log *slog.Logger) *Server {
	if cfg.MaxBatchSize <= 0 {
		cfg.MaxBatchSize = 5461
	}
	if cfg.MaxPayloadBytes <= 0 {
		cfg.MaxPayloadBytes = 40 << 20
	}
	if cfg.Version == "" {
		cfg.Version = "1.0.0"
	}
	if provider == nil {
		provider = auth.None{}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{store: st, auth: provider, cfg: cfg, log: log}
}

type ctxKey int

const identityKey ctxKey = 1

func identity(r *http.Request) *auth.Identity {
	id, _ := r.Context().Value(identityKey).(*auth.Identity)
	return id
}

// handlerFunc is an HTTP handler that returns an error.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *Server) h(fn handlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := fn(w, r); err != nil {
			s.writeError(w, r, err)
		}
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apierr.Error
	if !errors.As(err, &ae) {
		if errors.Is(err, context.Canceled) {
			ae = &apierr.Error{Status: 499, Name: "ChromaError", Message: "request cancelled"}
		} else {
			s.log.Error("internal error", "method", r.Method, "path", r.URL.Path, "err", err)
			ae = apierr.Internal("%s", err.Error())
		}
	}
	writeJSON(w, ae.Status, map[string]string{"error": ae.Name, "message": ae.Message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status = http.StatusInternalServerError
		b = []byte(`{"error":"InternalError","message":"failed to encode response"}`)
	}
	writeRaw(w, status, b)
}

func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

// readBody reads the request body, enforcing the payload size limit. An
// empty body decodes as an empty JSON object.
func (s *Server) readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, s.cfg.MaxPayloadBytes+1))
	if err != nil {
		return nil, apierr.InvalidArgument("Failed to read request body: %s", err.Error())
	}
	if int64(len(b)) > s.cfg.MaxPayloadBytes {
		return nil, apierr.PayloadTooLarge("Payload too large")
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}

// decodeBody decodes a JSON body into v, mapping failures to Chroma's 422.
func (s *Server) decodeBody(r *http.Request, v any) error {
	b, err := s.readBody(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return apierr.Unprocessable("%s", describeJSONError(err))
	}
	return nil
}

func describeJSONError(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		if te.Field != "" {
			return te.Field + ": invalid type: " + te.Value + ", expected " + te.Type.String()
		}
		return "invalid type: " + te.Value + ", expected " + te.Type.String()
	}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return "expected value: " + se.Error()
	}
	return err.Error()
}

func newTraceID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// middleware: trace id, panic recovery, request logging.
func (s *Server) base(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("chroma-trace-id", newTraceID())
		sw := &statusWriter{ResponseWriter: w, status: 200}
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic", "err", rec, "stack", string(debug.Stack()))
				s.writeError(sw, r, apierr.Internal("internal server error"))
			}
			s.log.Debug("request", "method", r.Method, "path", r.URL.Path, "status", sw.status, "dur", time.Since(start))
		}()
		next.ServeHTTP(sw, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) cors(next http.Handler) http.Handler {
	if len(s.cfg.CORSOrigins) == 0 {
		return next
	}
	allowAll := len(s.cfg.CORSOrigins) == 1 && s.cfg.CORSOrigins[0] == "*"
	allowed := map[string]bool{}
	for _, o := range s.cfg.CORSOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowAll || allowed[origin]) {
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Add("Vary", "Origin")
			}
			w.Header().Set("Access-Control-Allow-Methods", "*")
			w.Header().Set("Access-Control-Allow-Headers", "*")
			w.Header().Set("Access-Control-Expose-Headers", "chroma-trace-id")
		}
		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authn authenticates every API request except the unauthenticated system
// endpoints.
func (s *Server) authn(next http.Handler) http.Handler {
	open := map[string]bool{
		"/api/v2": true, "/api/v2/": true, "/api/v2/heartbeat": true, "/api/v2/healthcheck": true,
		"/api/v2/version": true, "/api/v2/pre-flight-checks": true,
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if open[r.URL.Path] || !strings.HasPrefix(r.URL.Path, "/api/v2") {
			next.ServeHTTP(w, r)
			return
		}
		id, err := s.auth.Authenticate(r)
		if err != nil {
			s.writeError(w, r, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), identityKey, id)))
	})
}

// authorize checks tenant/database access for the authenticated identity.
func authorize(r *http.Request, tenant, database string) error {
	id := identity(r)
	if id == nil || id.CanAccess(tenant, database) {
		return nil
	}
	return apierr.Forbidden("Forbidden")
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(s.base, s.cors, s.authn)
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })

	r.HandleFunc("/api/v1/*", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusGone, map[string]string{"error": "Unimplemented", "message": "The v1 API is deprecated. Please use /v2 apis"})
	})

	r.Get("/api/v2", s.h(s.heartbeat))
	r.Get("/api/v2/heartbeat", s.h(s.heartbeat))
	r.Get("/api/v2/healthcheck", s.h(s.healthcheck))
	r.Get("/api/v2/version", s.h(s.version))
	r.Get("/api/v2/pre-flight-checks", s.h(s.preflight))
	r.Get("/api/v2/auth/identity", s.h(s.getIdentity))
	r.Post("/api/v2/reset", s.h(s.reset))

	r.Get("/api/v2/collections/{crn}", s.h(s.getCollectionByCRN))

	r.Post("/api/v2/tenants", s.h(s.createTenant))
	r.Get("/api/v2/tenants/{tenant}", s.h(s.getTenant))
	r.Patch("/api/v2/tenants/{tenant}", s.h(s.updateTenant))

	r.Route("/api/v2/tenants/{tenant}/databases", func(r chi.Router) {
		r.Get("/", s.h(s.listDatabases))
		r.Post("/", s.h(s.createDatabase))
		r.Get("/by-id/{database_id}", s.h(s.getDatabaseByID))
		r.Get("/{database}", s.h(s.getDatabase))
		r.Delete("/{database}", s.h(s.deleteDatabase))
		r.Get("/{database}/collections_count", s.h(s.countCollections))
		r.Route("/{database}/collections", func(r chi.Router) {
			r.Get("/", s.h(s.listCollections))
			r.Post("/", s.h(s.createCollection))
			r.Get("/by-id/{collection_id}", s.h(s.getCollectionByID))
			r.Get("/{collection_id}", s.h(s.getCollection))
			r.Put("/{collection_id}", s.h(s.updateCollection))
			r.Delete("/{collection_id}", s.h(s.deleteCollection))
			r.Post("/{collection_id}/add", s.h(s.add))
			r.Post("/{collection_id}/update", s.h(s.update))
			r.Post("/{collection_id}/upsert", s.h(s.upsert))
			r.Post("/{collection_id}/delete", s.h(s.deleteRecords))
			r.Get("/{collection_id}/count", s.h(s.count))
			r.Post("/{collection_id}/get", s.h(s.get))
			r.Post("/{collection_id}/query", s.h(s.query))
			r.Post("/{collection_id}/search", s.h(s.search))
			r.Get("/{collection_id}/indexing_status", s.h(s.indexingStatus))
			r.Post("/{collection_id}/fork", s.h(s.fork))
			r.Get("/{collection_id}/fork_count", s.h(s.forkCount))
			r.Post("/{collection_id}/functions/attach", s.h(s.unsupportedFunctions))
			r.Get("/{collection_id}/functions/{function_name}", s.h(s.unsupportedFunctions))
			r.Post("/{collection_id}/attached_functions/{name}/add_input", s.h(s.unsupportedFunctions))
			r.Post("/{collection_id}/attached_functions/{name}/detach", s.h(s.unsupportedFunctions))
		})
	})

	r.Get("/openapi.json", s.openapi)
	r.Get("/docs", s.docs)
	r.Get("/docs/", s.docs)
	return r
}
