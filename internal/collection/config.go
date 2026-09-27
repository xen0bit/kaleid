package collection

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/wire"
)

// HnswConfiguration is the API-level HNSW config (CollectionConfiguration.hnsw).
type HnswConfiguration struct {
	Space          *Space   `json:"space,omitempty"`
	EfConstruction *int     `json:"ef_construction,omitempty"`
	EfSearch       *int     `json:"ef_search,omitempty"`
	MaxNeighbors   *int     `json:"max_neighbors,omitempty"`
	NumThreads     *int     `json:"num_threads,omitempty"`
	ResizeFactor   *float64 `json:"resize_factor,omitempty"`
	SyncThreshold  *int     `json:"sync_threshold,omitempty"`
	BatchSize      *int     `json:"batch_size,omitempty"`
}

// Configuration is the API CollectionConfiguration payload.
type Configuration struct {
	Hnsw              *HnswConfiguration `json:"hnsw"`
	Spann             json.RawMessage    `json:"spann"`
	EmbeddingFunction json.RawMessage    `json:"embedding_function"`
}

// UpdateHnswConfiguration lists the mutable HNSW params.
type UpdateHnswConfiguration struct {
	EfSearch      *int     `json:"ef_search,omitempty"`
	MaxNeighbors  *int     `json:"max_neighbors,omitempty"`
	NumThreads    *int     `json:"num_threads,omitempty"`
	ResizeFactor  *float64 `json:"resize_factor,omitempty"`
	SyncThreshold *int     `json:"sync_threshold,omitempty"`
	BatchSize     *int     `json:"batch_size,omitempty"`
}

// UpdateSpannConfiguration lists the mutable SPANN params.
type UpdateSpannConfiguration struct {
	SearchNprobe *int `json:"search_nprobe,omitempty"`
	EfSearch     *int `json:"ef_search,omitempty"`
}

// UpdateConfiguration is the API UpdateCollectionConfiguration payload.
type UpdateConfiguration struct {
	Hnsw              *UpdateHnswConfiguration  `json:"hnsw"`
	Spann             *UpdateSpannConfiguration `json:"spann"`
	EmbeddingFunction json.RawMessage           `json:"embedding_function"`
}

// Hnsw is a fully-populated internal HNSW configuration.
type Hnsw struct {
	Space          Space
	EfConstruction int
	EfSearch       int
	MaxNeighbors   int
	NumThreads     int
	ResizeFactor   float64
	SyncThreshold  int
	BatchSize      int
}

// DefaultHnsw returns the default internal HNSW config.
func DefaultHnsw() Hnsw {
	return Hnsw{
		Space: DefaultSpace, EfConstruction: DefaultEfConstruction, EfSearch: DefaultEfSearch,
		MaxNeighbors: DefaultM, NumThreads: DefaultNumThreads(), ResizeFactor: DefaultResizeFactor,
		SyncThreshold: DefaultSyncThreshold, BatchSize: DefaultBatchSize,
	}
}

func (h Hnsw) isDefault() bool {
	d := DefaultHnsw()
	return h.EfConstruction == d.EfConstruction && h.EfSearch == d.EfSearch &&
		h.MaxNeighbors == d.MaxNeighbors && h.NumThreads == d.NumThreads &&
		h.BatchSize == d.BatchSize && h.SyncThreshold == d.SyncThreshold &&
		h.ResizeFactor == d.ResizeFactor && h.Space == d.Space
}

func hnswFromAPI(c *HnswConfiguration) Hnsw {
	h := DefaultHnsw()
	if c == nil {
		return h
	}
	if c.Space != nil {
		h.Space = *c.Space
	}
	if c.EfConstruction != nil {
		h.EfConstruction = *c.EfConstruction
	}
	if c.EfSearch != nil {
		h.EfSearch = *c.EfSearch
	}
	if c.MaxNeighbors != nil {
		h.MaxNeighbors = *c.MaxNeighbors
	}
	if c.NumThreads != nil {
		h.NumThreads = *c.NumThreads
	}
	if c.ResizeFactor != nil {
		h.ResizeFactor = *c.ResizeFactor
	}
	if c.SyncThreshold != nil {
		h.SyncThreshold = *c.SyncThreshold
	}
	if c.BatchSize != nil {
		h.BatchSize = *c.BatchSize
	}
	return h
}

// Internal is InternalCollectionConfiguration (HNSW only; SPANN requests are
// converted to HNSW exactly as local Chroma does).
type Internal struct {
	Hnsw              Hnsw
	EmbeddingFunction json.RawMessage
}

func (c Internal) isDefault() bool {
	return isEFDefault(c.EmbeddingFunction) && c.Hnsw.isDefault()
}

var errLegacy = apierr.InvalidArgument("Failed to parse hnsw parameters from segment metadata")

// hnswFromLegacyMetadata parses "hnsw:*" collection metadata keys.
func hnswFromLegacyMetadata(md wire.Metadata) (Hnsw, error) {
	h := DefaultHnsw()
	if md == nil {
		return h, nil
	}
	getInt := func(v wire.Value) (int, bool) {
		if v.Kind == wire.KindInt && v.Int >= 0 {
			return int(v.Int), true
		}
		return 0, false
	}
	for k, v := range md {
		if !strings.HasPrefix(k, "hnsw:") {
			continue
		}
		var ok bool
		switch k {
		case "hnsw:space":
			if v.Kind == wire.KindString && Space(v.Str).Valid() {
				h.Space, ok = Space(v.Str), true
			}
		case "hnsw:construction_ef":
			h.EfConstruction, ok = getInt(v)
		case "hnsw:search_ef":
			h.EfSearch, ok = getInt(v)
		case "hnsw:M":
			h.MaxNeighbors, ok = getInt(v)
		case "hnsw:num_threads":
			h.NumThreads, ok = getInt(v)
		case "hnsw:resize_factor":
			if v.IsNumber() {
				h.ResizeFactor, ok = v.AsFloat(), true
			}
		case "hnsw:sync_threshold":
			h.SyncThreshold, ok = getInt(v)
		case "hnsw:batch_size":
			h.BatchSize, ok = getInt(v)
		}
		if !ok {
			return h, errLegacy
		}
	}
	if h.SyncThreshold < 2 || h.BatchSize < 2 {
		return h, errLegacy
	}
	return h, nil
}

// ValidateEmbeddingFunction checks the tagged-enum shape of an EF config.
func ValidateEmbeddingFunction(ef json.RawMessage) error {
	if len(ef) == 0 || bytes.Equal(bytes.TrimSpace(ef), []byte("null")) {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(ef, &m); err != nil {
		return fmt.Errorf("embedding_function: invalid type: expected internally tagged enum EmbeddingFunctionConfiguration")
	}
	var t string
	if err := json.Unmarshal(m["type"], &t); err != nil {
		return fmt.Errorf("embedding_function: missing field `type`")
	}
	switch t {
	case "legacy", "unknown":
		return nil
	case "known":
		var name string
		if err := json.Unmarshal(m["name"], &name); err != nil {
			return fmt.Errorf("embedding_function: missing field `name`")
		}
		if _, ok := m["config"]; !ok {
			return fmt.Errorf("embedding_function: missing field `config`")
		}
		return nil
	}
	return fmt.Errorf("embedding_function: unknown variant `%s`, expected one of `legacy`, `known`, `unknown`", t)
}

func spannSpace(raw json.RawMessage) *Space {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	var s struct {
		Space *Space `json:"space"`
	}
	_ = json.Unmarshal(raw, &s)
	return s.Space
}

func hasRaw(r json.RawMessage) bool {
	return len(r) > 0 && !bytes.Equal(bytes.TrimSpace(r), []byte("null"))
}

// FromConfig mirrors InternalCollectionConfiguration::try_from_config with
// the HNSW default KNN index.
func FromConfig(c *Configuration, md wire.Metadata) (Internal, error) {
	if c == nil {
		c = &Configuration{}
	}
	hnswCfg := c.Hnsw
	hasSpann := hasRaw(c.Spann)
	if hnswCfg == nil && !hasSpann {
		h, err := hnswFromLegacyMetadata(md)
		if err != nil {
			return Internal{}, err
		}
		return Internal{Hnsw: h, EmbeddingFunction: c.EmbeddingFunction}, nil
	}
	if hnswCfg != nil && hasSpann {
		return Internal{}, apierr.InvalidArgument("Multiple vector index configurations provided")
	}
	if hnswCfg != nil {
		h := hnswFromAPI(hnswCfg)
		if h == DefaultHnsw() {
			legacy, err := hnswFromLegacyMetadata(md)
			if err != nil {
				return Internal{}, err
			}
			h = legacy
		}
		return Internal{Hnsw: h, EmbeddingFunction: c.EmbeddingFunction}, nil
	}
	h := DefaultHnsw()
	if sp := spannSpace(c.Spann); sp != nil {
		h.Space = *sp
	}
	return Internal{Hnsw: h, EmbeddingFunction: c.EmbeddingFunction}, nil
}

// SchemaFromInternal mirrors TryFrom<&InternalCollectionConfiguration> for Schema.
func SchemaFromInternal(c Internal) *Schema {
	s := NewDefaultSchema()
	vc := VectorIndexConfig{
		Space:             ptr(c.Hnsw.Space),
		EmbeddingFunction: c.EmbeddingFunction,
		Hnsw: &HnswIndexConfig{
			EfConstruction: ptr(c.Hnsw.EfConstruction), MaxNeighbors: ptr(c.Hnsw.MaxNeighbors),
			EfSearch: ptr(c.Hnsw.EfSearch), NumThreads: ptr(c.Hnsw.NumThreads),
			BatchSize: ptr(c.Hnsw.BatchSize), SyncThreshold: ptr(c.Hnsw.SyncThreshold),
			ResizeFactor: ptr(c.Hnsw.ResizeFactor),
		},
	}
	s.Defaults.FloatList.VectorIndex.Config = vc
	ec := vc
	ec.SourceKey = ptr(DocumentKey)
	hc := *vc.Hnsw
	ec.Hnsw = &hc
	s.Keys[EmbeddingKey].FloatList.VectorIndex.Config = ec
	return s
}

// setEF sets the embedding function on the defaults and #embedding vector index.
func (s *Schema) setEF(ef json.RawMessage) {
	if s.Defaults.FloatList != nil && s.Defaults.FloatList.VectorIndex != nil {
		s.Defaults.FloatList.VectorIndex.Config.EmbeddingFunction = ef
	}
	if vt := s.Keys[EmbeddingKey]; vt != nil && vt.FloatList != nil && vt.FloatList.VectorIndex != nil {
		vt.FloatList.VectorIndex.Config.EmbeddingFunction = ef
	}
}

// Reconcile mirrors Schema::reconcile_schema_and_config for creation.
func Reconcile(user *Schema, cfg Internal) (*Schema, error) {
	if user != nil && !user.IsDefault() && !cfg.isDefault() {
		return nil, apierr.InvalidArgument("Failed to reconcile schema: Cannot set both collection config and schema simultaneously")
	}
	reconciled := ReconcileWithDefaults(user)
	if cfg.isDefault() {
		if reconciled.IsDefault() {
			s := NewDefaultSchema()
			if hasRaw(cfg.EmbeddingFunction) {
				s.setEF(cfg.EmbeddingFunction)
			}
			return s, nil
		}
		return reconciled, nil
	}
	return SchemaFromInternal(cfg), nil
}

// ToInternal mirrors TryFrom<&Schema> for InternalCollectionConfiguration.
func (s *Schema) ToInternal() Internal {
	vi := s.EmbeddingVectorIndex()
	h := DefaultHnsw()
	var ef json.RawMessage
	if vi != nil {
		if vi.Config.Space != nil {
			h.Space = *vi.Config.Space
		}
		if hc := vi.Config.Hnsw; hc != nil {
			if hc.EfConstruction != nil {
				h.EfConstruction = *hc.EfConstruction
			}
			if hc.MaxNeighbors != nil {
				h.MaxNeighbors = *hc.MaxNeighbors
			}
			if hc.EfSearch != nil {
				h.EfSearch = *hc.EfSearch
			}
			if hc.NumThreads != nil {
				h.NumThreads = *hc.NumThreads
			}
			if hc.BatchSize != nil {
				h.BatchSize = *hc.BatchSize
			}
			if hc.SyncThreshold != nil {
				h.SyncThreshold = *hc.SyncThreshold
			}
			if hc.ResizeFactor != nil {
				h.ResizeFactor = *hc.ResizeFactor
			}
		}
		ef = vi.Config.EmbeddingFunction
	}
	return Internal{Hnsw: h, EmbeddingFunction: ef}
}

// ConfigurationJSON renders the configuration_json response object.
func (s *Schema) ConfigurationJSON() json.RawMessage {
	c := s.ToInternal()
	vi := s.EmbeddingVectorIndex()
	type hnswOut struct {
		Space          Space   `json:"space"`
		EfConstruction int     `json:"ef_construction"`
		EfSearch       int     `json:"ef_search"`
		MaxNeighbors   int     `json:"max_neighbors"`
		ResizeFactor   float64 `json:"resize_factor"`
		SyncThreshold  int     `json:"sync_threshold"`
	}
	out := struct {
		Hnsw              *hnswOut        `json:"hnsw"`
		Spann             json.RawMessage `json:"spann"`
		EmbeddingFunction json.RawMessage `json:"embedding_function"`
	}{Spann: json.RawMessage("null"), EmbeddingFunction: json.RawMessage("null")}
	if vi != nil && hasRaw(vi.Config.Spann) && vi.Config.Hnsw == nil {
		out.Spann = vi.Config.Spann
	} else {
		out.Hnsw = &hnswOut{c.Hnsw.Space, c.Hnsw.EfConstruction, c.Hnsw.EfSearch, c.Hnsw.MaxNeighbors, c.Hnsw.ResizeFactor, c.Hnsw.SyncThreshold}
	}
	if hasRaw(c.EmbeddingFunction) {
		out.EmbeddingFunction = c.EmbeddingFunction
	}
	b, _ := json.Marshal(out)
	return b
}

// ApplyUpdate mirrors Schema::update for PUT new_configuration.
func (s *Schema) ApplyUpdate(u *UpdateConfiguration) {
	apply := func(vi *VectorIndexType) {
		if vi == nil {
			return
		}
		if u.Hnsw != nil {
			if vi.Config.Hnsw == nil {
				vi.Config.Hnsw = defaultHnswIndexConfig()
			}
			h := vi.Config.Hnsw
			if u.Hnsw.EfSearch != nil {
				h.EfSearch = ptr(*u.Hnsw.EfSearch)
			}
			if u.Hnsw.MaxNeighbors != nil {
				h.MaxNeighbors = ptr(*u.Hnsw.MaxNeighbors)
			}
			if u.Hnsw.NumThreads != nil {
				h.NumThreads = ptr(*u.Hnsw.NumThreads)
			}
			if u.Hnsw.ResizeFactor != nil {
				h.ResizeFactor = ptr(*u.Hnsw.ResizeFactor)
			}
			if u.Hnsw.SyncThreshold != nil {
				h.SyncThreshold = ptr(*u.Hnsw.SyncThreshold)
			}
			if u.Hnsw.BatchSize != nil {
				h.BatchSize = ptr(*u.Hnsw.BatchSize)
			}
		}
		if hasRaw(u.EmbeddingFunction) {
			vi.Config.EmbeddingFunction = u.EmbeddingFunction
		}
	}
	if s.Defaults.FloatList != nil {
		apply(s.Defaults.FloatList.VectorIndex)
	}
	if vt := s.Keys[EmbeddingKey]; vt != nil && vt.FloatList != nil {
		apply(vt.FloatList.VectorIndex)
	}
}

// Validate mirrors validators::validate_schema for user-provided schemas.
func (s *Schema) Validate() error {
	fail := func(msg string) error { return apierr.Validation("schema", "%s", msg) }
	if d := s.Defaults.FloatList; d != nil && d.VectorIndex != nil && d.VectorIndex.Enabled {
		return fail("Vector index cannot be enabled by default. It can only be enabled on #embedding field.")
	}
	if d := s.Defaults.SparseVector; d != nil && d.SparseVectorIndex != nil && d.SparseVectorIndex.Enabled {
		return fail("Sparse vector index cannot be enabled by default. Please enable sparse vector index on specific keys.")
	}
	if d := s.Defaults.String; d != nil && d.FtsIndex != nil && d.FtsIndex.Enabled {
		return fail("Full text search / regular expression index cannot be enabled by default. It can only be enabled on #document field.")
	}
	for key, vt := range s.Keys {
		if vt == nil {
			continue
		}
		if strings.HasPrefix(key, "#") && key != EmbeddingKey && key != DocumentKey {
			return fail(fmt.Sprintf("key cannot begin with '#'. Keys starting with '#' are reserved for system use: %s", key))
		}
		if key == DocumentKey && (vt.FloatList != nil || vt.Float != nil || vt.Int != nil || vt.Bool != nil || vt.SparseVector != nil) {
			return fail(fmt.Sprintf("Document field cannot have any value types other than string: %s", key))
		}
		if key == EmbeddingKey && (vt.String != nil || vt.Float != nil || vt.Int != nil || vt.Bool != nil || vt.SparseVector != nil) {
			return fail(fmt.Sprintf("Embedding field cannot have any value types other than float_list: %s", key))
		}
		if key != EmbeddingKey && vt.FloatList != nil && vt.FloatList.VectorIndex != nil && vt.FloatList.VectorIndex.Enabled {
			return fail(fmt.Sprintf("Vector index can only be enabled on #embedding field: %s", key))
		}
		if key == EmbeddingKey && vt.FloatList != nil && vt.FloatList.VectorIndex != nil {
			if sk := vt.FloatList.VectorIndex.Config.SourceKey; sk != nil && *sk != DocumentKey {
				return fail("Vector index can only source from #document")
			}
		}
		if key != DocumentKey && vt.String != nil && vt.String.FtsIndex != nil && vt.String.FtsIndex.Enabled {
			return fail(fmt.Sprintf("Full text search / regular expression index can only be enabled on #document field: %s", key))
		}
		if key == DocumentKey && vt.String != nil && vt.String.StringInvertedIndex != nil && vt.String.StringInvertedIndex.Enabled {
			return fail(fmt.Sprintf("String inverted index can not be enabled on #document key: %s", key))
		}
	}
	return nil
}
