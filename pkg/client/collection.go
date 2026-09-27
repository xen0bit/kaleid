package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Collection is a handle to a collection. Its fields reflect the server's
// state when the handle was fetched.
type Collection struct {
	ID       string
	Name     string
	Tenant   string
	Database string
	Metadata Metadata
	// Dimension is nil until the first embedding is written.
	Dimension *int
	// Configuration and Schema are the server's configuration_json and
	// schema documents, passed through unchanged.
	Configuration json.RawMessage
	Schema        json.RawMessage

	c *Client
}

type collectionJSON struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Tenant        string          `json:"tenant"`
	Database      string          `json:"database"`
	Metadata      Metadata        `json:"metadata"`
	Dimension     *int            `json:"dimension"`
	Configuration json.RawMessage `json:"configuration_json"`
	Schema        json.RawMessage `json:"schema"`
}

func (c *Client) collection(j collectionJSON) *Collection {
	return &Collection{
		ID: j.ID, Name: j.Name, Tenant: j.Tenant, Database: j.Database, Metadata: j.Metadata,
		Dimension: j.Dimension, Configuration: j.Configuration, Schema: j.Schema,
		c: c.Scoped(j.Tenant, j.Database),
	}
}

// Space is a distance function.
type Space string

// Distance functions.
const (
	SpaceL2     Space = "l2"
	SpaceCosine Space = "cosine"
	SpaceIP     Space = "ip"
)

// HNSWConfig configures a collection's HNSW index. Zero fields use server
// defaults.
type HNSWConfig struct {
	Space          Space   `json:"space,omitempty"`
	EfConstruction int     `json:"ef_construction,omitempty"`
	EfSearch       int     `json:"ef_search,omitempty"`
	MaxNeighbors   int     `json:"max_neighbors,omitempty"`
	ResizeFactor   float64 `json:"resize_factor,omitempty"`
	SyncThreshold  int     `json:"sync_threshold,omitempty"`
}

// CreateCollectionOptions configure CreateCollection.
type CreateCollectionOptions struct {
	Metadata Metadata
	HNSW     *HNSWConfig
	// EmbeddingFunction records the embedding function configuration for
	// other clients, e.g. {"type":"known","name":"openai","config":{...}}.
	EmbeddingFunction json.RawMessage
	// Schema is an optional Chroma schema document.
	Schema json.RawMessage
	// GetOrCreate returns the existing collection instead of failing when
	// the name is taken.
	GetOrCreate bool
}

// CreateCollection creates a collection (or fetches it with GetOrCreate).
func (c *Client) CreateCollection(ctx context.Context, name string, opts *CreateCollectionOptions) (*Collection, error) {
	body := map[string]any{"name": name}
	if opts != nil {
		if opts.Metadata != nil {
			body["metadata"] = opts.Metadata
		}
		if opts.HNSW != nil || opts.EmbeddingFunction != nil {
			cfg := map[string]any{}
			if opts.HNSW != nil {
				cfg["hnsw"] = opts.HNSW
			}
			if opts.EmbeddingFunction != nil {
				cfg["embedding_function"] = opts.EmbeddingFunction
			}
			body["configuration"] = cfg
		}
		if opts.Schema != nil {
			body["schema"] = opts.Schema
		}
		body["get_or_create"] = opts.GetOrCreate
	}
	var j collectionJSON
	if err := c.do(ctx, http.MethodPost, c.dbPath()+"/collections", body, &j); err != nil {
		return nil, err
	}
	return c.collection(j), nil
}

// GetCollection fetches a collection by name.
func (c *Client) GetCollection(ctx context.Context, name string) (*Collection, error) {
	var j collectionJSON
	if err := c.do(ctx, http.MethodGet, c.dbPath()+"/collections/"+esc(name), nil, &j); err != nil {
		return nil, err
	}
	return c.collection(j), nil
}

// GetCollectionByID fetches a collection by id.
func (c *Client) GetCollectionByID(ctx context.Context, id string) (*Collection, error) {
	var j collectionJSON
	if err := c.do(ctx, http.MethodGet, c.dbPath()+"/collections/by-id/"+esc(id), nil, &j); err != nil {
		return nil, err
	}
	return c.collection(j), nil
}

// ListCollections lists collections. limit <= 0 means no limit.
func (c *Client) ListCollections(ctx context.Context, limit, offset int) ([]*Collection, error) {
	var js []collectionJSON
	if err := c.do(ctx, http.MethodGet, c.dbPath()+"/collections"+paging(limit, offset), nil, &js); err != nil {
		return nil, err
	}
	out := make([]*Collection, len(js))
	for i, j := range js {
		out[i] = c.collection(j)
	}
	return out, nil
}

// CountCollections counts collections in the database.
func (c *Client) CountCollections(ctx context.Context) (int, error) {
	var n int
	err := c.do(ctx, http.MethodGet, c.dbPath()+"/collections_count", nil, &n)
	return n, err
}

// DeleteCollection deletes a collection by name.
func (c *Client) DeleteCollection(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, c.dbPath()+"/collections/"+esc(name), nil, nil)
}

func (col *Collection) path(suffix string) string {
	return col.c.dbPath() + "/collections/" + esc(col.ID) + suffix
}

// ModifyOptions change a collection. Nil fields are left unchanged.
type ModifyOptions struct {
	Name *string
	// Metadata replaces the collection metadata entirely.
	Metadata Metadata
	// EfSearch updates the HNSW search breadth.
	EfSearch *int
	// EmbeddingFunction replaces the stored embedding function config.
	EmbeddingFunction json.RawMessage
}

// Modify renames the collection, replaces its metadata, or updates its
// configuration. The handle is updated on success.
func (col *Collection) Modify(ctx context.Context, opts ModifyOptions) error {
	body := map[string]any{}
	if opts.Name != nil {
		body["new_name"] = *opts.Name
	}
	if opts.Metadata != nil {
		body["new_metadata"] = opts.Metadata
	}
	if opts.EfSearch != nil || opts.EmbeddingFunction != nil {
		cfg := map[string]any{}
		if opts.EfSearch != nil {
			cfg["hnsw"] = map[string]int{"ef_search": *opts.EfSearch}
		}
		if opts.EmbeddingFunction != nil {
			cfg["embedding_function"] = opts.EmbeddingFunction
		}
		body["new_configuration"] = cfg
	}
	if err := col.c.do(ctx, http.MethodPut, col.path(""), body, nil); err != nil {
		return err
	}
	if opts.Name != nil {
		col.Name = *opts.Name
	}
	if opts.Metadata != nil {
		col.Metadata = opts.Metadata
	}
	return nil
}

// Records is a batch of records. IDs are required; every other slice is
// optional but, when set, must have one entry per id.
type Records struct {
	IDs        []string
	Embeddings [][]float32
	Documents  []string
	URIs       []string
	Metadatas  []Metadata
}

func (r Records) body(allowNullEmbeddings bool) (map[string]any, error) {
	n := len(r.IDs)
	for name, l := range map[string]int{"Embeddings": len(r.Embeddings), "Documents": len(r.Documents), "URIs": len(r.URIs), "Metadatas": len(r.Metadatas)} {
		if l != 0 && l != n {
			return nil, fmt.Errorf("client: %s has %d entries, want %d (one per id)", name, l, n)
		}
	}
	body := map[string]any{"ids": r.IDs}
	if r.Embeddings != nil {
		if !allowNullEmbeddings {
			for i, e := range r.Embeddings {
				if e == nil {
					return nil, fmt.Errorf("client: embedding %d is nil", i)
				}
			}
		}
		body["embeddings"] = base64Embeddings(r.Embeddings)
	}
	if r.Documents != nil {
		body["documents"] = r.Documents
	}
	if r.URIs != nil {
		body["uris"] = r.URIs
	}
	if r.Metadatas != nil {
		body["metadatas"] = r.Metadatas
	}
	return body, nil
}

// Add inserts records. Ids that already exist are ignored.
func (col *Collection) Add(ctx context.Context, r Records) error {
	if r.Embeddings == nil {
		return fmt.Errorf("client: Add requires embeddings")
	}
	body, err := r.body(false)
	if err != nil {
		return err
	}
	return col.c.do(ctx, http.MethodPost, col.path("/add"), body, nil)
}

// Upsert inserts new records and updates existing ones. Metadata is merged;
// a nil metadata value deletes that key.
func (col *Collection) Upsert(ctx context.Context, r Records) error {
	if r.Embeddings == nil {
		return fmt.Errorf("client: Upsert requires embeddings")
	}
	body, err := r.body(false)
	if err != nil {
		return err
	}
	return col.c.do(ctx, http.MethodPost, col.path("/upsert"), body, nil)
}

// Update changes existing records; unknown ids are ignored. A nil entry in
// Embeddings keeps that record's embedding. Metadata is merged; a nil
// metadata value deletes that key.
func (col *Collection) Update(ctx context.Context, r Records) error {
	body, err := r.body(true)
	if err != nil {
		return err
	}
	return col.c.do(ctx, http.MethodPost, col.path("/update"), body, nil)
}

// DeleteOptions select records to delete. With neither IDs nor a filter,
// nothing is deleted.
type DeleteOptions struct {
	IDs           []string
	Where         Where
	WhereDocument WhereDocument
	// Limit caps the number of records deleted by a filter.
	Limit int
}

// Delete removes records and returns the number deleted.
func (col *Collection) Delete(ctx context.Context, opts DeleteOptions) (int, error) {
	body := map[string]any{}
	if opts.IDs != nil {
		body["ids"] = opts.IDs
	}
	if opts.Where != nil {
		body["where"] = opts.Where
	}
	if opts.WhereDocument != nil {
		body["where_document"] = opts.WhereDocument
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	var out struct {
		Deleted int `json:"deleted"`
	}
	err := col.c.do(ctx, http.MethodPost, col.path("/delete"), body, &out)
	return out.Deleted, err
}

// Count returns the number of records.
func (col *Collection) Count(ctx context.Context) (int, error) {
	var n int
	err := col.c.do(ctx, http.MethodGet, col.path("/count"), nil, &n)
	return n, err
}

// GetOptions select records to fetch. With no IDs and no filter, all
// records are returned (paged by Limit/Offset) in insertion order.
type GetOptions struct {
	IDs           []string
	Where         Where
	WhereDocument WhereDocument
	Limit         int
	Offset        int
	// Include defaults to documents and metadatas.
	Include []Include
}

// GetResult holds fetched records. Columns not requested are nil.
type GetResult struct {
	IDs        []string
	Embeddings [][]float32
	Documents  []*string
	URIs       []*string
	Metadatas  []Metadata
	Include    []Include
}

// Get fetches records.
func (col *Collection) Get(ctx context.Context, opts GetOptions) (*GetResult, error) {
	body := map[string]any{}
	if opts.IDs != nil {
		body["ids"] = opts.IDs
	}
	if opts.Where != nil {
		body["where"] = opts.Where
	}
	if opts.WhereDocument != nil {
		body["where_document"] = opts.WhereDocument
	}
	if opts.Limit > 0 {
		body["limit"] = opts.Limit
	}
	if opts.Offset > 0 {
		body["offset"] = opts.Offset
	}
	if opts.Include != nil {
		body["include"] = opts.Include
	}
	var raw struct {
		IDs        []string   `json:"ids"`
		Embeddings []float32s `json:"embeddings"`
		Documents  []*string  `json:"documents"`
		URIs       []*string  `json:"uris"`
		Metadatas  []Metadata `json:"metadatas"`
		Include    []Include  `json:"include"`
	}
	if err := col.c.do(ctx, http.MethodPost, col.path("/get"), body, &raw); err != nil {
		return nil, err
	}
	return &GetResult{IDs: raw.IDs, Embeddings: toFloat32Matrix(raw.Embeddings), Documents: raw.Documents,
		URIs: raw.URIs, Metadatas: raw.Metadatas, Include: raw.Include}, nil
}

// QueryOptions configure a nearest-neighbour query.
type QueryOptions struct {
	// Embeddings holds one or more query vectors.
	Embeddings [][]float32
	// NResults per query (server default 10 when zero).
	NResults int
	// IDs restricts the search to these records.
	IDs           []string
	Where         Where
	WhereDocument WhereDocument
	// Include defaults to documents, metadatas and distances.
	Include []Include
}

// QueryResult holds one result list per query embedding. Columns not
// requested are nil.
type QueryResult struct {
	IDs        [][]string
	Embeddings [][][]float32
	Documents  [][]*string
	URIs       [][]*string
	Metadatas  [][]Metadata
	Distances  [][]float32
	Include    []Include
}

// Query finds the nearest records to each query embedding.
func (col *Collection) Query(ctx context.Context, opts QueryOptions) (*QueryResult, error) {
	body := map[string]any{"query_embeddings": opts.Embeddings}
	if opts.NResults > 0 {
		body["n_results"] = opts.NResults
	}
	if opts.IDs != nil {
		body["ids"] = opts.IDs
	}
	if opts.Where != nil {
		body["where"] = opts.Where
	}
	if opts.WhereDocument != nil {
		body["where_document"] = opts.WhereDocument
	}
	if opts.Include != nil {
		body["include"] = opts.Include
	}
	var raw struct {
		IDs        [][]string      `json:"ids"`
		Embeddings [][]float32s    `json:"embeddings"`
		Documents  [][]*string     `json:"documents"`
		URIs       [][]*string     `json:"uris"`
		Metadatas  [][]Metadata    `json:"metadatas"`
		Distances  [][]nullableF32 `json:"distances"`
		Include    []Include       `json:"include"`
	}
	if err := col.c.do(ctx, http.MethodPost, col.path("/query"), body, &raw); err != nil {
		return nil, err
	}
	out := &QueryResult{IDs: raw.IDs, Documents: raw.Documents, URIs: raw.URIs, Metadatas: raw.Metadatas, Include: raw.Include}
	if raw.Embeddings != nil {
		out.Embeddings = make([][][]float32, len(raw.Embeddings))
		for i, e := range raw.Embeddings {
			out.Embeddings[i] = toFloat32Matrix(e)
		}
	}
	if raw.Distances != nil {
		out.Distances = make([][]float32, len(raw.Distances))
		for i, row := range raw.Distances {
			out.Distances[i] = make([]float32, len(row))
			for j, d := range row {
				out.Distances[i][j] = float32(d)
			}
		}
	}
	return out, nil
}

// nullableF32 decodes a single JSON number (or null) into a float.
type nullableF32 float32

func (f *nullableF32) UnmarshalJSON(b []byte) error {
	var n *json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	if n == nil {
		*f = 0
		return nil
	}
	v, err := n.Float64()
	*f = nullableF32(v)
	return err
}

// Fork copies the collection into a new collection (Kaleid and Chroma Cloud).
func (col *Collection) Fork(ctx context.Context, newName string) (*Collection, error) {
	var j collectionJSON
	if err := col.c.do(ctx, http.MethodPost, col.path("/fork"), map[string]string{"new_name": newName}, &j); err != nil {
		return nil, err
	}
	return col.c.collection(j), nil
}

// IndexingStatus reports index progress (Kaleid and Chroma Cloud).
type IndexingStatus struct {
	OpIndexingProgress float64 `json:"op_indexing_progress"`
	NumUnindexedOps    int64   `json:"num_unindexed_ops"`
	NumIndexedOps      int64   `json:"num_indexed_ops"`
	TotalOps           int64   `json:"total_ops"`
}

// IndexingStatus returns the collection's indexing progress.
func (col *Collection) IndexingStatus(ctx context.Context) (*IndexingStatus, error) {
	var s IndexingStatus
	if err := col.c.do(ctx, http.MethodGet, col.path("/indexing_status"), nil, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
