package collection

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/xen0bit/kaleid/internal/wire"
)

// referenceDefaultSchema is the schema Chroma 1.5.9 returns for a collection
// created with metadata {"hnsw:space": "cosine"} (num_threads normalized).
const referenceDefaultSchema = `{"defaults":{"string":{"fts_index":{"enabled":false,"config":{}},"string_inverted_index":{"enabled":true,"config":{}}},"float_list":{"vector_index":{"enabled":false,"config":{"space":"cosine","hnsw":{"ef_construction":100,"max_neighbors":16,"ef_search":100,"num_threads":NT,"batch_size":100,"sync_threshold":1000,"resize_factor":1.2}}}},"sparse_vector":{"sparse_vector_index":{"enabled":false,"config":{"embedding_function":{"type":"unknown"},"bm25":false}}},"int":{"int_inverted_index":{"enabled":true,"config":{}}},"float":{"float_inverted_index":{"enabled":true,"config":{}}},"bool":{"bool_inverted_index":{"enabled":true,"config":{}}}},"keys":{"#embedding":{"float_list":{"vector_index":{"enabled":true,"config":{"space":"cosine","source_key":"#document","hnsw":{"ef_construction":100,"max_neighbors":16,"ef_search":100,"num_threads":NT,"batch_size":100,"sync_threshold":1000,"resize_factor":1.2}}}}},"#document":{"string":{"fts_index":{"enabled":true,"config":{}},"string_inverted_index":{"enabled":false,"config":{}}}}}}`

func jsonEqual(t *testing.T, a, b []byte) {
	t.Helper()
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &y); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatalf("JSON mismatch:\n got %s\nwant %s", a, b)
	}
}

func TestLegacyMetadataSchemaMatchesReference(t *testing.T) {
	md := wire.Metadata{"hnsw:space": wire.StringValue("cosine")}
	cfg, err := FromConfig(nil, md)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Reconcile(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(s)
	want := strings.ReplaceAll(referenceDefaultSchema, "NT", jsonInt(DefaultNumThreads()))
	jsonEqual(t, got, []byte(want))
	jsonEqual(t, s.ConfigurationJSON(), []byte(`{"hnsw":{"space":"cosine","ef_construction":100,"ef_search":100,"max_neighbors":16,"resize_factor":1.2,"sync_threshold":1000},"spann":null,"embedding_function":null}`))
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestConfigurationVariants(t *testing.T) {
	space := SpaceIP
	cfg, err := FromConfig(&Configuration{Hnsw: &HnswConfiguration{Space: &space, EfSearch: ptr(50)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := Reconcile(nil, cfg)
	jsonEqual(t, s.ConfigurationJSON(), []byte(`{"hnsw":{"space":"ip","ef_construction":100,"ef_search":50,"max_neighbors":16,"resize_factor":1.2,"sync_threshold":1000},"spann":null,"embedding_function":null}`))

	// SPANN requests become HNSW with the same space (local Chroma behaviour).
	cfg, err = FromConfig(&Configuration{Spann: json.RawMessage(`{"space":"cosine"}`)}, nil)
	if err != nil || cfg.Hnsw.Space != SpaceCosine {
		t.Fatalf("spann conversion: %+v %v", cfg, err)
	}

	if _, err := FromConfig(&Configuration{Hnsw: &HnswConfiguration{}, Spann: json.RawMessage(`{}`)}, nil); err == nil ||
		!strings.Contains(err.Error(), "Multiple vector index configurations provided") {
		t.Fatalf("expected multiple config error, got %v", err)
	}
	if _, err := FromConfig(nil, wire.Metadata{"hnsw:bogus": wire.IntValue(1)}); err == nil {
		t.Fatal("expected legacy metadata error")
	}
	cfg, _ = FromConfig(nil, wire.Metadata{"hnsw:space": wire.StringValue("ip"), "hnsw:M": wire.IntValue(32)})
	if cfg.Hnsw.Space != SpaceIP || cfg.Hnsw.MaxNeighbors != 32 {
		t.Fatalf("legacy metadata not applied: %+v", cfg.Hnsw)
	}
}

func TestEmbeddingFunctionRoundTrip(t *testing.T) {
	ef := json.RawMessage(`{"type":"known","name":"openai","config":{"model_name":"m"}}`)
	cfg, _ := FromConfig(&Configuration{EmbeddingFunction: ef}, nil)
	s, _ := Reconcile(nil, cfg)
	var out struct {
		EmbeddingFunction json.RawMessage `json:"embedding_function"`
	}
	_ = json.Unmarshal(s.ConfigurationJSON(), &out)
	jsonEqual(t, out.EmbeddingFunction, ef)
}

func TestApplyUpdateAndKeyGrowth(t *testing.T) {
	s := NewDefaultSchema()
	s.ApplyUpdate(&UpdateConfiguration{Hnsw: &UpdateHnswConfiguration{EfSearch: ptr(77)}})
	if s.ToInternal().Hnsw.EfSearch != 77 {
		t.Fatal("ef_search update not applied")
	}
	if !s.EnsureKey("k", "int") || s.EnsureKey("k", "int") || !s.EnsureKey("k", "float") {
		t.Fatal("unexpected EnsureKey results")
	}
	if s.EnsureKey("#hidden", "int") {
		t.Fatal("reserved keys must not be added")
	}
}

func TestSchemaValidation(t *testing.T) {
	s := NewDefaultSchema()
	s.Keys["#other"] = &ValueTypes{}
	if err := s.Validate(); err == nil {
		t.Fatal("expected reserved key error")
	}
}
