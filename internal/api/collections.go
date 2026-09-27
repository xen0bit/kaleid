package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
	"github.com/xen0bit/kaleid/internal/store"
	"github.com/xen0bit/kaleid/internal/validate"
	"github.com/xen0bit/kaleid/internal/wire"
)

// collectionJSON renders a Collection like Chroma's API.
func collectionJSON(c store.Collection) map[string]any {
	return map[string]any{
		"id":                 c.ID.String(),
		"name":               c.Name,
		"configuration_json": json.RawMessage(c.Schema.ConfigurationJSON()),
		"schema":             c.Schema,
		"metadata":           c.Metadata,
		"dimension":          c.Dimension,
		"tenant":             c.Tenant,
		"database":           c.Database,
		"log_position":       0,
		"version":            c.Version,
	}
}

// scope extracts and validates the tenant/database path parameters.
func scope(r *http.Request) (string, string, error) {
	tenant, db := chi.URLParam(r, "tenant"), chi.URLParam(r, "database")
	if err := validate.DatabasePathName(db); err != nil {
		return "", "", err
	}
	if err := authorize(r, tenant, db); err != nil {
		return "", "", err
	}
	return tenant, db, nil
}

func parseCollectionID(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "collection_id"))
	if err != nil {
		return id, apierr.InvalidArgument("Collection ID is not a valid UUIDv4")
	}
	return id, nil
}

// loadCollection resolves the collection for record-level endpoints.
func (s *Server) loadCollection(r *http.Request) (store.Collection, error) {
	tenant, db, err := scope(r)
	if err != nil {
		return store.Collection{}, err
	}
	id, err := parseCollectionID(r)
	if err != nil {
		return store.Collection{}, err
	}
	c, err := s.store.GetCollectionByID(r.Context(), tenant, db, id)
	if err != nil {
		if ae, ok := err.(*apierr.Error); ok && ae.Status == http.StatusNotFound {
			return c, apierr.NotFound("Collection [%s] does not exist.", id)
		}
		return c, err
	}
	return c, nil
}

func (s *Server) listCollections(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	limit, offset, err := parsePaging(r)
	if err != nil {
		return err
	}
	cs, err := s.store.ListCollections(r.Context(), tenant, db, limit, offset)
	if err != nil {
		return err
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, collectionJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
	return nil
}

func (s *Server) countCollections(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	n, err := s.store.CountCollections(r.Context(), tenant, db)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, n)
	return nil
}

// decodeStrict decodes raw into v rejecting unknown fields.
func decodeStrict(raw json.RawMessage, v any, path string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		msg := err.Error()
		if strings.HasPrefix(msg, "json: unknown field ") {
			field := strings.Trim(strings.TrimPrefix(msg, "json: unknown field "), `"`)
			return apierr.Unprocessable("%s.%s: unknown field `%s`", path, field, field)
		}
		return apierr.Unprocessable("%s: %s", path, describeJSONError(err))
	}
	return nil
}

func isNullRaw(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func parseConfiguration(raw json.RawMessage, path string) (*collection.Configuration, error) {
	if isNullRaw(raw) {
		return nil, nil
	}
	var parts struct {
		Hnsw              json.RawMessage `json:"hnsw"`
		Spann             json.RawMessage `json:"spann"`
		EmbeddingFunction json.RawMessage `json:"embedding_function"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil, apierr.Unprocessable("%s: %s", path, describeJSONError(err))
	}
	cfg := &collection.Configuration{}
	if !isNullRaw(parts.Hnsw) {
		cfg.Hnsw = &collection.HnswConfiguration{}
		if err := decodeStrict(parts.Hnsw, cfg.Hnsw, path+".hnsw"); err != nil {
			return nil, err
		}
		if cfg.Hnsw.Space != nil && !cfg.Hnsw.Space.Valid() {
			return nil, apierr.Unprocessable("%s.hnsw.space: unknown variant `%s`, expected one of `l2`, `cosine`, `ip`", path, *cfg.Hnsw.Space)
		}
	}
	if !isNullRaw(parts.Spann) {
		var sp map[string]json.RawMessage
		if err := json.Unmarshal(parts.Spann, &sp); err != nil {
			return nil, apierr.Unprocessable("%s.spann: invalid type, expected struct SpannConfiguration", path)
		}
		cfg.Spann = parts.Spann
	}
	if !isNullRaw(parts.EmbeddingFunction) {
		if err := collection.ValidateEmbeddingFunction(parts.EmbeddingFunction); err != nil {
			return nil, apierr.Unprocessable("%s.%s", path, err.Error())
		}
		cfg.EmbeddingFunction = parts.EmbeddingFunction
	}
	return cfg, nil
}

func (s *Server) createCollection(w http.ResponseWriter, r *http.Request) error {
	var p struct {
		Name          *string         `json:"name"`
		Metadata      json.RawMessage `json:"metadata"`
		Configuration json.RawMessage `json:"configuration"`
		Schema        json.RawMessage `json:"schema"`
		GetOrCreate   bool            `json:"get_or_create"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.Name == nil {
		return apierr.Unprocessable("missing field `name`")
	}
	md, err := wire.ParseMetadata(p.Metadata)
	if err != nil {
		return apierr.Unprocessable("metadata.%s", err.Error())
	}
	cfg, err := parseConfiguration(p.Configuration, "configuration")
	if err != nil {
		return err
	}
	var userSchema *collection.Schema
	if !isNullRaw(p.Schema) {
		userSchema = &collection.Schema{}
		if err := json.Unmarshal(p.Schema, userSchema); err != nil {
			return apierr.Unprocessable("schema: %s", describeJSONError(err))
		}
		if userSchema.Keys == nil {
			userSchema.Keys = map[string]*collection.ValueTypes{}
		}
		if err := userSchema.Validate(); err != nil {
			return err
		}
	}
	internal, err := collection.FromConfig(cfg, md)
	if err != nil {
		return err
	}
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	if err := validate.Name(*p.Name); err != nil {
		return err
	}
	if err := validate.CollectionMetadata(md, "metadata"); err != nil {
		return err
	}
	schema, err := collection.Reconcile(userSchema, internal)
	if err != nil {
		return err
	}
	c, err := s.store.CreateCollection(r.Context(), store.CreateCollectionParams{
		Tenant: tenant, Database: db, Name: *p.Name, Metadata: md, Schema: schema, GetOrCreate: p.GetOrCreate,
	})
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collectionJSON(c))
	return nil
}

func (s *Server) getCollection(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	c, err := s.store.GetCollectionByName(r.Context(), tenant, db, chi.URLParam(r, "collection_id"))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collectionJSON(c))
	return nil
}

func (s *Server) getCollectionByID(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	id, err := parseCollectionID(r)
	if err != nil {
		return err
	}
	c, err := s.store.GetCollectionByID(r.Context(), tenant, db, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collectionJSON(c))
	return nil
}

// getCollectionByCRN resolves "tenant_resource_name:database:collection".
func (s *Server) getCollectionByCRN(w http.ResponseWriter, r *http.Request) error {
	crn := chi.URLParam(r, "crn")
	parts := strings.Split(crn, ":")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return apierr.InvalidArgument("Invalid CRN format. Expected format: <tenant_resource_name>:<database_name>:<collection_name>")
	}
	tenant := parts[0]
	if t, err := s.store.TenantByResourceName(r.Context(), parts[0]); err == nil {
		tenant = t
	}
	if err := authorize(r, tenant, parts[1]); err != nil {
		return err
	}
	c, err := s.store.GetCollectionByName(r.Context(), tenant, parts[1], parts[2])
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collectionJSON(c))
	return nil
}

func (s *Server) updateCollection(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	var p struct {
		NewName          *string         `json:"new_name"`
		NewMetadata      json.RawMessage `json:"new_metadata"`
		NewConfiguration json.RawMessage `json:"new_configuration"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	var params store.UpdateCollectionParams
	if !isNullRaw(p.NewMetadata) {
		upd, err := wire.ParseUpdateMetadata(p.NewMetadata)
		if err != nil {
			return apierr.Unprocessable("new_metadata.%s", err.Error())
		}
		md := upd.ToMetadata()
		if len(upd) == 0 {
			md = wire.Metadata{}
		}
		params.NewMetadata = md
		params.SetMetadata = true
	}
	if !isNullRaw(p.NewConfiguration) {
		var parts struct {
			Hnsw              json.RawMessage `json:"hnsw"`
			Spann             json.RawMessage `json:"spann"`
			EmbeddingFunction json.RawMessage `json:"embedding_function"`
		}
		if err := json.Unmarshal(p.NewConfiguration, &parts); err != nil {
			return apierr.Unprocessable("new_configuration: %s", describeJSONError(err))
		}
		u := &collection.UpdateConfiguration{}
		if !isNullRaw(parts.Hnsw) {
			u.Hnsw = &collection.UpdateHnswConfiguration{}
			if err := decodeStrict(parts.Hnsw, u.Hnsw, "new_configuration.hnsw"); err != nil {
				return err
			}
		}
		if !isNullRaw(parts.Spann) {
			u.Spann = &collection.UpdateSpannConfiguration{}
			if err := decodeStrict(parts.Spann, u.Spann, "new_configuration.spann"); err != nil {
				return err
			}
		}
		if !isNullRaw(parts.EmbeddingFunction) {
			if err := collection.ValidateEmbeddingFunction(parts.EmbeddingFunction); err != nil {
				return apierr.Unprocessable("new_configuration.%s", err.Error())
			}
			u.EmbeddingFunction = parts.EmbeddingFunction
		}
		if u.Hnsw != nil && u.Spann != nil {
			return apierr.InvalidArgument("Multiple vector index configurations provided")
		}
		params.Configuration = u
	}
	id, err := parseCollectionID(r)
	if err != nil {
		return err
	}
	if p.NewName != nil {
		if err := validate.Name(*p.NewName); err != nil {
			return err
		}
		params.NewName = p.NewName
	}
	if params.SetMetadata {
		if err := validate.CollectionMetadata(params.NewMetadata, "new_metadata"); err != nil {
			return err
		}
	}
	if err := s.store.UpdateCollection(r.Context(), tenant, db, id, params); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}

func (s *Server) deleteCollection(w http.ResponseWriter, r *http.Request) error {
	tenant, db, err := scope(r)
	if err != nil {
		return err
	}
	if err := s.store.DeleteCollection(r.Context(), tenant, db, chi.URLParam(r, "collection_id")); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, struct{}{})
	return nil
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	var p struct {
		NewName *string `json:"new_name"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.NewName == nil {
		return apierr.Unprocessable("missing field `new_name`")
	}
	if err := validate.Name(*p.NewName); err != nil {
		return err
	}
	forked, err := s.store.Fork(r.Context(), c, *p.NewName)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, collectionJSON(forked))
	return nil
}

func (s *Server) forkCount(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	n, err := s.store.ForkCount(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]int{"count": n})
	return nil
}

func (s *Server) indexingStatus(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	total, err := s.store.WriteSeq(r.Context(), c)
	if err != nil {
		return err
	}
	// PostgreSQL indexes synchronously: every operation is indexed on commit.
	writeJSON(w, http.StatusOK, map[string]any{
		"op_indexing_progress": json.RawMessage("1.0"), "num_unindexed_ops": 0, "num_indexed_ops": total, "total_ops": total,
	})
	return nil
}

func (s *Server) unsupportedFunctions(w http.ResponseWriter, r *http.Request) error {
	return apierr.Unimplemented("Attached functions are not supported by Kaleid")
}
