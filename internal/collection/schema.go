// Package collection implements Chroma's collection configuration and schema
// model: defaults, user-schema merging, legacy "hnsw:*" metadata, the
// configuration <-> schema conversions, and metadata-driven key growth.
package collection

import (
	"bytes"
	"encoding/json"
	"runtime"
)

const (
	EmbeddingKey = "#embedding"
	DocumentKey  = "#document"
)

// Space is a distance function.
type Space string

const (
	SpaceL2     Space = "l2"
	SpaceCosine Space = "cosine"
	SpaceIP     Space = "ip"
)

// Valid reports whether s is a known distance space.
func (s Space) Valid() bool { return s == SpaceL2 || s == SpaceCosine || s == SpaceIP }

// Defaults mirroring chroma_types.
const (
	DefaultEfConstruction = 100
	DefaultEfSearch       = 100
	DefaultM              = 16
	DefaultResizeFactor   = 1.2
	DefaultSyncThreshold  = 1000
	DefaultBatchSize      = 100
	DefaultSpace          = SpaceL2
)

// DefaultNumThreads mirrors std::thread::available_parallelism.
func DefaultNumThreads() int { return runtime.NumCPU() }

func ptr[T any](v T) *T { return &v }

// HnswIndexConfig is the schema-level HNSW config (all fields optional).
type HnswIndexConfig struct {
	EfConstruction *int     `json:"ef_construction,omitempty"`
	MaxNeighbors   *int     `json:"max_neighbors,omitempty"`
	EfSearch       *int     `json:"ef_search,omitempty"`
	NumThreads     *int     `json:"num_threads,omitempty"`
	BatchSize      *int     `json:"batch_size,omitempty"`
	SyncThreshold  *int     `json:"sync_threshold,omitempty"`
	ResizeFactor   *float64 `json:"resize_factor,omitempty"`
}

func defaultHnswIndexConfig() *HnswIndexConfig {
	return &HnswIndexConfig{
		EfConstruction: ptr(DefaultEfConstruction),
		MaxNeighbors:   ptr(DefaultM),
		EfSearch:       ptr(DefaultEfSearch),
		NumThreads:     ptr(DefaultNumThreads()),
		BatchSize:      ptr(DefaultBatchSize),
		SyncThreshold:  ptr(DefaultSyncThreshold),
		ResizeFactor:   ptr(DefaultResizeFactor),
	}
}

func (h *HnswIndexConfig) isDefault() bool {
	eq := func(p *int, d int) bool { return p == nil || *p == d }
	return eq(h.EfConstruction, DefaultEfConstruction) && eq(h.MaxNeighbors, DefaultM) &&
		eq(h.EfSearch, DefaultEfSearch) && eq(h.BatchSize, DefaultBatchSize) &&
		eq(h.SyncThreshold, DefaultSyncThreshold) &&
		(h.ResizeFactor == nil || *h.ResizeFactor == DefaultResizeFactor)
}

// VectorIndexConfig is a vector index configuration.
type VectorIndexConfig struct {
	Space             *Space           `json:"space,omitempty"`
	EmbeddingFunction json.RawMessage  `json:"embedding_function,omitempty"`
	SourceKey         *string          `json:"source_key,omitempty"`
	Hnsw              *HnswIndexConfig `json:"hnsw,omitempty"`
	Spann             json.RawMessage  `json:"spann,omitempty"`
}

// VectorIndexType wraps an enabled flag and config.
type VectorIndexType struct {
	Enabled bool              `json:"enabled"`
	Config  VectorIndexConfig `json:"config"`
}

// SparseVectorIndexConfig is a sparse vector index configuration.
type SparseVectorIndexConfig struct {
	EmbeddingFunction json.RawMessage `json:"embedding_function,omitempty"`
	SourceKey         *string         `json:"source_key,omitempty"`
	Bm25              *bool           `json:"bm25,omitempty"`
	Algorithm         *string         `json:"algorithm,omitempty"`
}

// SparseVectorIndexType wraps an enabled flag and config.
type SparseVectorIndexType struct {
	Enabled bool                    `json:"enabled"`
	Config  SparseVectorIndexConfig `json:"config"`
}

// SimpleIndexType is an index type whose config carries no parameters we use.
type SimpleIndexType struct {
	Enabled bool            `json:"enabled"`
	Config  json.RawMessage `json:"config"`
}

func simple(enabled bool) *SimpleIndexType {
	return &SimpleIndexType{Enabled: enabled, Config: json.RawMessage("{}")}
}

type StringValueType struct {
	FtsIndex            *SimpleIndexType `json:"fts_index,omitempty"`
	StringInvertedIndex *SimpleIndexType `json:"string_inverted_index,omitempty"`
}
type FloatListValueType struct {
	VectorIndex *VectorIndexType `json:"vector_index,omitempty"`
}
type SparseVectorValueType struct {
	SparseVectorIndex *SparseVectorIndexType `json:"sparse_vector_index,omitempty"`
}
type IntValueType struct {
	IntInvertedIndex *SimpleIndexType `json:"int_inverted_index,omitempty"`
}
type FloatValueType struct {
	FloatInvertedIndex *SimpleIndexType `json:"float_inverted_index,omitempty"`
}
type BoolValueType struct {
	BoolInvertedIndex *SimpleIndexType `json:"bool_inverted_index,omitempty"`
}

// ValueTypes holds per-type index configuration.
type ValueTypes struct {
	String       *StringValueType       `json:"string,omitempty"`
	FloatList    *FloatListValueType    `json:"float_list,omitempty"`
	SparseVector *SparseVectorValueType `json:"sparse_vector,omitempty"`
	Int          *IntValueType          `json:"int,omitempty"`
	Float        *FloatValueType        `json:"float,omitempty"`
	Bool         *BoolValueType         `json:"bool,omitempty"`
}

// UnmarshalJSON accepts both "string" and the "#string" aliases.
func (v *ValueTypes) UnmarshalJSON(b []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	for k, val := range raw {
		key := k
		if len(key) > 0 && key[0] == '#' {
			key = key[1:]
		}
		var err error
		switch key {
		case "string":
			err = unmarshalOpt(val, &v.String)
		case "float_list":
			err = unmarshalOpt(val, &v.FloatList)
		case "sparse_vector":
			err = unmarshalOpt(val, &v.SparseVector)
		case "int":
			err = unmarshalOpt(val, &v.Int)
		case "float":
			err = unmarshalOpt(val, &v.Float)
		case "bool":
			err = unmarshalOpt(val, &v.Bool)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func unmarshalOpt[T any](raw json.RawMessage, dst **T) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		*dst = nil
		return nil
	}
	var t T
	if err := json.Unmarshal(raw, &t); err != nil {
		return err
	}
	*dst = &t
	return nil
}

// Schema is Chroma's collection schema.
type Schema struct {
	Defaults                 ValueTypes             `json:"defaults"`
	Keys                     map[string]*ValueTypes `json:"keys"`
	Cmek                     json.RawMessage        `json:"cmek,omitempty"`
	SourceAttachedFunctionID *string                `json:"source_attached_function_id,omitempty"`
}

// NewDefaultSchema mirrors Schema::new_default(KnnIndex::Hnsw).
func NewDefaultSchema() *Schema {
	vectorCfg := VectorIndexConfig{Space: ptr(DefaultSpace), Hnsw: defaultHnswIndexConfig()}
	s := &Schema{
		Defaults: ValueTypes{
			String:    &StringValueType{StringInvertedIndex: simple(true), FtsIndex: simple(false)},
			Float:     &FloatValueType{FloatInvertedIndex: simple(true)},
			Int:       &IntValueType{IntInvertedIndex: simple(true)},
			Bool:      &BoolValueType{BoolInvertedIndex: simple(true)},
			FloatList: &FloatListValueType{VectorIndex: &VectorIndexType{Enabled: false, Config: vectorCfg}},
			SparseVector: &SparseVectorValueType{SparseVectorIndex: &SparseVectorIndexType{
				Enabled: false,
				Config: SparseVectorIndexConfig{
					EmbeddingFunction: json.RawMessage(`{"type":"unknown"}`),
					Bm25:              ptr(false),
				},
			}},
		},
		Keys: map[string]*ValueTypes{},
	}
	embCfg := VectorIndexConfig{Space: ptr(DefaultSpace), SourceKey: ptr(DocumentKey), Hnsw: defaultHnswIndexConfig()}
	s.Keys[EmbeddingKey] = &ValueTypes{FloatList: &FloatListValueType{VectorIndex: &VectorIndexType{Enabled: true, Config: embCfg}}}
	s.Keys[DocumentKey] = &ValueTypes{String: &StringValueType{FtsIndex: simple(true), StringInvertedIndex: simple(false)}}
	return s
}

// Clone deep-copies the schema via JSON.
func (s *Schema) Clone() *Schema {
	b, _ := json.Marshal(s)
	var out Schema
	_ = json.Unmarshal(b, &out)
	if out.Keys == nil {
		out.Keys = map[string]*ValueTypes{}
	}
	return &out
}

// EmbeddingVectorIndex returns the vector index for #embedding, falling back
// to the defaults.
func (s *Schema) EmbeddingVectorIndex() *VectorIndexType {
	if vt := s.Keys[EmbeddingKey]; vt != nil && vt.FloatList != nil && vt.FloatList.VectorIndex != nil {
		return vt.FloatList.VectorIndex
	}
	if s.Defaults.FloatList != nil {
		return s.Defaults.FloatList.VectorIndex
	}
	return nil
}

// SparseIndexKeys returns metadata keys with an enabled sparse vector index.
func (s *Schema) SparseIndexKeys() map[string]*SparseVectorIndexType {
	out := map[string]*SparseVectorIndexType{}
	for k, vt := range s.Keys {
		if vt != nil && vt.SparseVector != nil && vt.SparseVector.SparseVectorIndex != nil && vt.SparseVector.SparseVectorIndex.Enabled {
			out[k] = vt.SparseVector.SparseVectorIndex
		}
	}
	return out
}

func isEFDefault(ef json.RawMessage) bool {
	if len(ef) == 0 || bytes.Equal(ef, []byte("null")) {
		return true
	}
	var m struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if json.Unmarshal(ef, &m) != nil {
		return false
	}
	switch m.Type {
	case "unknown":
		return true
	case "known":
		return m.Name == "default"
	}
	return false
}

func isSpaceDefault(s *Space) bool { return s == nil || *s == DefaultSpace }

func isVectorCfgDefault(c *VectorIndexConfig) bool {
	switch {
	case c.Hnsw != nil && len(c.Spann) == 0:
		return c.Hnsw.isDefault()
	case c.Hnsw == nil && len(c.Spann) > 0:
		return false
	case c.Hnsw != nil && len(c.Spann) > 0:
		return false
	}
	return true
}

func isValueTypesDefault(v *ValueTypes) bool {
	if v.String != nil {
		if v.String.StringInvertedIndex != nil && !v.String.StringInvertedIndex.Enabled {
			return false
		}
		if v.String.FtsIndex != nil && v.String.FtsIndex.Enabled {
			return false
		}
	}
	if v.Float != nil && v.Float.FloatInvertedIndex != nil && !v.Float.FloatInvertedIndex.Enabled {
		return false
	}
	if v.Int != nil && v.Int.IntInvertedIndex != nil && !v.Int.IntInvertedIndex.Enabled {
		return false
	}
	if v.Bool != nil && v.Bool.BoolInvertedIndex != nil && !v.Bool.BoolInvertedIndex.Enabled {
		return false
	}
	if v.FloatList != nil && v.FloatList.VectorIndex != nil {
		vi := v.FloatList.VectorIndex
		if vi.Enabled || !isEFDefault(vi.Config.EmbeddingFunction) || !isSpaceDefault(vi.Config.Space) ||
			vi.Config.SourceKey != nil || !isVectorCfgDefault(&vi.Config) {
			return false
		}
	}
	if v.SparseVector != nil && v.SparseVector.SparseVectorIndex != nil {
		si := v.SparseVector.SparseVectorIndex
		if si.Enabled || !isEFDefault(si.Config.EmbeddingFunction) || si.Config.SourceKey != nil ||
			(si.Config.Bm25 != nil && *si.Config.Bm25) {
			return false
		}
	}
	return true
}

func isEmbeddingValueTypesDefault(v *ValueTypes) bool {
	if v.String != nil || v.Float != nil || v.Int != nil || v.Bool != nil || v.SparseVector != nil {
		return false
	}
	if v.FloatList != nil && v.FloatList.VectorIndex != nil {
		vi := v.FloatList.VectorIndex
		if !vi.Enabled || !isSpaceDefault(vi.Config.Space) || !isEFDefault(vi.Config.EmbeddingFunction) ||
			vi.Config.SourceKey == nil || *vi.Config.SourceKey != DocumentKey || !isVectorCfgDefault(&vi.Config) {
			return false
		}
	}
	return true
}

func isDocumentValueTypesDefault(v *ValueTypes) bool {
	if v.FloatList != nil || v.Float != nil || v.Int != nil || v.Bool != nil || v.SparseVector != nil {
		return false
	}
	if v.String != nil {
		if v.String.FtsIndex != nil && !v.String.FtsIndex.Enabled {
			return false
		}
		if v.String.StringInvertedIndex != nil && v.String.StringInvertedIndex.Enabled {
			return false
		}
	}
	return true
}

// IsDefault mirrors Schema::is_default.
func (s *Schema) IsDefault() bool {
	if !isValueTypesDefault(&s.Defaults) {
		return false
	}
	for k := range s.Keys {
		if k != EmbeddingKey && k != DocumentKey {
			return false
		}
	}
	if v := s.Keys[EmbeddingKey]; v != nil && !isEmbeddingValueTypesDefault(v) {
		return false
	}
	if v := s.Keys[DocumentKey]; v != nil && !isDocumentValueTypesDefault(v) {
		return false
	}
	return len(s.Cmek) == 0 || bytes.Equal(s.Cmek, []byte("null"))
}

// ---------------------------------------------------------------------------
// Merging (reconcile_with_defaults)
// ---------------------------------------------------------------------------

func mergeSimple(def, user *SimpleIndexType) *SimpleIndexType {
	if user != nil {
		c := *user
		if len(c.Config) == 0 {
			c.Config = json.RawMessage("{}")
		}
		return &c
	}
	return def
}

func orRaw(a, b json.RawMessage) json.RawMessage {
	if len(a) > 0 && !bytes.Equal(a, []byte("null")) {
		return a
	}
	return b
}

func orPtr[T any](a, b *T) *T {
	if a != nil {
		return a
	}
	return b
}

func mergeHnsw(def, user *HnswIndexConfig) *HnswIndexConfig {
	switch {
	case def != nil && user != nil:
		return &HnswIndexConfig{
			EfConstruction: orPtr(user.EfConstruction, def.EfConstruction),
			MaxNeighbors:   orPtr(user.MaxNeighbors, def.MaxNeighbors),
			EfSearch:       orPtr(user.EfSearch, def.EfSearch),
			NumThreads:     orPtr(user.NumThreads, def.NumThreads),
			BatchSize:      orPtr(user.BatchSize, def.BatchSize),
			SyncThreshold:  orPtr(user.SyncThreshold, def.SyncThreshold),
			ResizeFactor:   orPtr(user.ResizeFactor, def.ResizeFactor),
		}
	case user != nil:
		return user
	}
	return def
}

func mergeVector(def, user *VectorIndexType) *VectorIndexType {
	switch {
	case def != nil && user != nil:
		return &VectorIndexType{Enabled: user.Enabled, Config: VectorIndexConfig{
			Space:             orPtr(user.Config.Space, def.Config.Space),
			EmbeddingFunction: orRaw(user.Config.EmbeddingFunction, def.Config.EmbeddingFunction),
			SourceKey:         orPtr(user.Config.SourceKey, def.Config.SourceKey),
			Hnsw:              mergeHnsw(def.Config.Hnsw, user.Config.Hnsw),
		}}
	case user != nil:
		return user
	}
	return def
}

func mergeSparse(def, user *SparseVectorIndexType) *SparseVectorIndexType {
	switch {
	case def != nil && user != nil:
		alg := def.Config.Algorithm
		if user.Config.Algorithm != nil && *user.Config.Algorithm != "wand" {
			alg = user.Config.Algorithm
		}
		return &SparseVectorIndexType{Enabled: user.Enabled, Config: SparseVectorIndexConfig{
			EmbeddingFunction: orRaw(user.Config.EmbeddingFunction, def.Config.EmbeddingFunction),
			SourceKey:         orPtr(user.Config.SourceKey, def.Config.SourceKey),
			Bm25:              orPtr(user.Config.Bm25, def.Config.Bm25),
			Algorithm:         alg,
		}}
	case user != nil:
		return user
	}
	return def
}

func mergeValueTypes(def, user *ValueTypes) *ValueTypes {
	out := &ValueTypes{}
	switch {
	case def.String != nil && user.String != nil:
		out.String = &StringValueType{
			FtsIndex:            mergeSimple(def.String.FtsIndex, user.String.FtsIndex),
			StringInvertedIndex: mergeSimple(def.String.StringInvertedIndex, user.String.StringInvertedIndex),
		}
	default:
		out.String = orPtr(user.String, def.String)
	}
	switch {
	case def.Float != nil && user.Float != nil:
		out.Float = &FloatValueType{FloatInvertedIndex: mergeSimple(def.Float.FloatInvertedIndex, user.Float.FloatInvertedIndex)}
	default:
		out.Float = orPtr(user.Float, def.Float)
	}
	switch {
	case def.Int != nil && user.Int != nil:
		out.Int = &IntValueType{IntInvertedIndex: mergeSimple(def.Int.IntInvertedIndex, user.Int.IntInvertedIndex)}
	default:
		out.Int = orPtr(user.Int, def.Int)
	}
	switch {
	case def.Bool != nil && user.Bool != nil:
		out.Bool = &BoolValueType{BoolInvertedIndex: mergeSimple(def.Bool.BoolInvertedIndex, user.Bool.BoolInvertedIndex)}
	default:
		out.Bool = orPtr(user.Bool, def.Bool)
	}
	switch {
	case def.FloatList != nil && user.FloatList != nil:
		out.FloatList = &FloatListValueType{VectorIndex: mergeVector(def.FloatList.VectorIndex, user.FloatList.VectorIndex)}
	default:
		out.FloatList = orPtr(user.FloatList, def.FloatList)
	}
	switch {
	case def.SparseVector != nil && user.SparseVector != nil:
		out.SparseVector = &SparseVectorValueType{SparseVectorIndex: mergeSparse(def.SparseVector.SparseVectorIndex, user.SparseVector.SparseVectorIndex)}
	default:
		out.SparseVector = orPtr(user.SparseVector, def.SparseVector)
	}
	return out
}

// ReconcileWithDefaults merges a user schema over the default schema.
func ReconcileWithDefaults(user *Schema) *Schema {
	def := NewDefaultSchema()
	if user == nil {
		return def
	}
	out := &Schema{
		Defaults: *mergeValueTypes(&def.Defaults, &user.Defaults),
		Keys:     map[string]*ValueTypes{},
		Cmek:     user.Cmek,
	}
	for k, v := range def.Keys {
		out.Keys[k] = v
	}
	for k, uv := range user.Keys {
		if uv == nil {
			continue
		}
		if dv, ok := out.Keys[k]; ok {
			out.Keys[k] = mergeValueTypes(dv, uv)
		} else {
			out.Keys[k] = uv
		}
	}
	return out
}

// EnsureKey adds a key/value-type entry derived from the defaults when a new
// metadata key or type is observed (Schema::ensure_key_from_metadata).
// Returns true when the schema changed.
func (s *Schema) EnsureKey(key, typeName string) bool {
	if len(key) > 0 && key[0] == '#' {
		return false
	}
	vt := s.Keys[key]
	if vt == nil {
		vt = &ValueTypes{}
		s.Keys[key] = vt
	}
	d := s.Defaults.Clone()
	switch typeName {
	case "bool":
		if vt.Bool == nil {
			vt.Bool = d.Bool
			return true
		}
	case "int":
		if vt.Int == nil {
			vt.Int = d.Int
			return true
		}
	case "float":
		if vt.Float == nil {
			vt.Float = d.Float
			return true
		}
	case "string":
		if vt.String == nil {
			vt.String = d.String
			return true
		}
	case "sparse_vector":
		if vt.SparseVector == nil {
			vt.SparseVector = d.SparseVector
			return true
		}
	}
	return false
}

// Clone deep-copies value types.
func (v *ValueTypes) Clone() *ValueTypes {
	b, _ := json.Marshal(v)
	var out ValueTypes
	_ = json.Unmarshal(b, &out)
	return &out
}
