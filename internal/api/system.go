package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/auth"
	"github.com/xen0bit/kaleid/internal/validate"
)

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) error {
	writeRaw(w, http.StatusOK, []byte(`{"nanosecond heartbeat":`+strconv.FormatInt(time.Now().UnixNano(), 10)+`}`))
	return nil
}

func (s *Server) healthcheck(w http.ResponseWriter, r *http.Request) error {
	if err := s.store.Ping(r.Context()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]bool{"is_executor_ready": false, "is_log_client_ready": false})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]bool{"is_executor_ready": true, "is_log_client_ready": true})
	return nil
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, s.cfg.Version)
	return nil
}

func (s *Server) preflight(w http.ResponseWriter, r *http.Request) error {
	writeJSON(w, http.StatusOK, map[string]any{"max_batch_size": s.cfg.MaxBatchSize, "supports_base64_encoding": true})
	return nil
}

func (s *Server) getIdentity(w http.ResponseWriter, r *http.Request) error {
	id := identity(r)
	out := auth.Identity{UserID: "", Tenant: "default_tenant", Databases: []string{"default_database"}}
	if id != nil {
		out.UserID = id.UserID
		if id.Tenant != auth.Wildcard {
			out.Tenant = id.Tenant
			out.Databases = []string{}
			for _, d := range id.Databases {
				if d != auth.Wildcard {
					out.Databases = append(out.Databases, d)
				}
			}
			if len(out.Databases) == 0 && len(id.Databases) > 0 {
				out.Databases = []string{"default_database"}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) reset(w http.ResponseWriter, r *http.Request) error {
	if id := identity(r); id != nil && (id.Tenant != auth.Wildcard) {
		return apierr.Forbidden("Forbidden")
	}
	if !s.cfg.AllowReset {
		return &apierr.Error{Status: http.StatusForbidden, Name: "ChromaError", Message: "Reset is disabled by config"}
	}
	if err := s.store.Reset(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, true)
	return nil
}

// ---------------------------------------------------------------------------
// Tenants
// ---------------------------------------------------------------------------

func (s *Server) createTenant(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Name *string `json:"name"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.Name == nil {
		return apierr.Unprocessable("missing field `name`")
	}
	if err := validate.TenantName(*p.Name); err != nil {
		return err
	}
	if err := authorize(r, *p.Name, ""); err != nil {
		return err
	}
	if err := s.store.CreateTenant(r.Context(), *p.Name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}

func (s *Server) getTenant(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "tenant")
	if err := authorize(r, name, ""); err != nil {
		return err
	}
	t, err := s.store.GetTenant(r.Context(), name)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": t.Name, "resource_name": t.ResourceName})
	return nil
}

func (s *Server) updateTenant(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "tenant")
	if err := authorize(r, name, ""); err != nil {
		return err
	}
	var p struct {
		ResourceName *string `json:"resource_name"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.ResourceName == nil {
		return apierr.Unprocessable("missing field `resource_name`")
	}
	if err := s.store.UpdateTenant(r.Context(), name, *p.ResourceName); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}

// ---------------------------------------------------------------------------
// Databases
// ---------------------------------------------------------------------------

func parsePaging(r *http.Request) (*int, int, error) {
	var limit *int
	offset := 0
	q := r.URL.Query()
	if v := q.Get("limit"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, 0, apierr.BadQuery("limit: invalid digit found in string")
		}
		l := int(n)
		limit = &l
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			return nil, 0, apierr.BadQuery("offset: invalid digit found in string")
		}
		offset = int(n)
	}
	return limit, offset, nil
}

func (s *Server) listDatabases(w http.ResponseWriter, r *http.Request) error {
	tenant := chi.URLParam(r, "tenant")
	if err := authorize(r, tenant, ""); err != nil {
		return err
	}
	limit, offset, err := parsePaging(r)
	if err != nil {
		return err
	}
	dbs, err := s.store.ListDatabases(r.Context(), tenant, limit, offset)
	if err != nil {
		return err
	}
	id := identity(r)
	out := make([]map[string]string, 0, len(dbs))
	for _, d := range dbs {
		if id != nil && !id.CanAccess(tenant, d.Name) {
			continue
		}
		out = append(out, map[string]string{"id": d.ID.String(), "name": d.Name, "tenant": d.Tenant})
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) createDatabase(w http.ResponseWriter, r *http.Request) error {
	tenant := chi.URLParam(r, "tenant")
	var p struct {
		Name *string `json:"name"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.Name == nil {
		return apierr.Unprocessable("missing field `name`")
	}
	if err := validate.DatabaseName(*p.Name); err != nil {
		return err
	}
	if err := authorize(r, tenant, *p.Name); err != nil {
		return err
	}
	if err := s.store.CreateDatabase(r.Context(), tenant, *p.Name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}

func (s *Server) getDatabase(w http.ResponseWriter, r *http.Request) error {
	tenant, db := chi.URLParam(r, "tenant"), chi.URLParam(r, "database")
	if err := authorize(r, tenant, db); err != nil {
		return err
	}
	d, err := s.store.GetDatabase(r.Context(), tenant, db)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": d.ID.String(), "name": d.Name, "tenant": d.Tenant})
	return nil
}

func (s *Server) getDatabaseByID(w http.ResponseWriter, r *http.Request) error {
	tenant := chi.URLParam(r, "tenant")
	id, err := uuid.Parse(chi.URLParam(r, "database_id"))
	if err != nil {
		return apierr.InvalidArgument("Invalid database id [%s]", chi.URLParam(r, "database_id"))
	}
	d, err := s.store.GetDatabaseByID(r.Context(), tenant, id)
	if err != nil {
		return err
	}
	if err := authorize(r, tenant, d.Name); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": d.ID.String(), "name": d.Name, "tenant": d.Tenant})
	return nil
}

func (s *Server) deleteDatabase(w http.ResponseWriter, r *http.Request) error {
	tenant, db := chi.URLParam(r, "tenant"), chi.URLParam(r, "database")
	if err := authorize(r, tenant, db); err != nil {
		return err
	}
	if err := s.store.DeleteDatabase(r.Context(), tenant, db); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}
