package client

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/xen0bit/kaleid/internal/wire"
)

// Special keys for Search selection, grouping and filtering.
const (
	KeyID        = "#id"
	KeyDocument  = "#document"
	KeyEmbedding = "#embedding"
	KeyMetadata  = "#metadata"
	KeyScore     = "#score"
)

// MarshalJSON encodes the tagged sparse vector format.
func (s SparseVector) MarshalJSON() ([]byte, error) {
	return wire.AppendSparse(nil, &wire.SparseVector{Indices: s.Indices, Values: s.Values, Tokens: s.Tokens}), nil
}

// Rank is a rank expression for Search. Lower scores rank first. Build ranks
// with Knn, Val and the arithmetic helpers.
type Rank map[string]any

// KnnOptions tune a KNN rank leaf.
type KnnOptions struct {
	// Key is the vector to search: KeyEmbedding (default) for dense
	// queries, or a metadata key holding sparse vectors.
	Key string
	// Limit is how many nearest records this leaf contributes (default 16).
	Limit int
	// Default is the score given to records outside this leaf's results.
	// Without it, such records are dropped from arithmetic combinations.
	Default *float64
	// ReturnRank scores records by rank position (0, 1, ...) instead of
	// distance, as used by reciprocal rank fusion.
	ReturnRank bool
}

// Knn ranks by distance to a dense query vector.
func Knn(query []float32, opts ...KnnOptions) Rank { return knn(query, opts) }

// KnnSparse ranks by sparse similarity (1 - dot product) on a metadata key.
func KnnSparse(query SparseVector, key string, opts ...KnnOptions) Rank {
	o := KnnOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	o.Key = key
	return knn(query, []KnnOptions{o})
}

func knn(query any, opts []KnnOptions) Rank {
	body := map[string]any{"query": query, "key": KeyEmbedding, "limit": 16}
	if len(opts) > 0 {
		o := opts[0]
		if o.Key != "" {
			body["key"] = o.Key
		}
		if o.Limit > 0 {
			body["limit"] = o.Limit
		}
		if o.Default != nil {
			body["default"] = *o.Default
		}
		if o.ReturnRank {
			body["return_rank"] = true
		}
	}
	return Rank{"$knn": body}
}

// Val is a constant.
func Val(v float64) Rank { return Rank{"$val": v} }

func list(op string, ranks []Rank) Rank {
	if ranks == nil {
		ranks = []Rank{}
	}
	return Rank{op: ranks}
}

// Sum adds ranks.
func Sum(ranks ...Rank) Rank { return list("$sum", ranks) }

// Mul multiplies ranks.
func Mul(ranks ...Rank) Rank { return list("$mul", ranks) }

// Max takes the maximum of ranks.
func Max(ranks ...Rank) Rank { return list("$max", ranks) }

// Min takes the minimum of ranks.
func Min(ranks ...Rank) Rank { return list("$min", ranks) }

// Sub subtracts right from left.
func Sub(left, right Rank) Rank { return Rank{"$sub": map[string]Rank{"left": left, "right": right}} }

// Div divides left by right.
func Div(left, right Rank) Rank { return Rank{"$div": map[string]Rank{"left": left, "right": right}} }

// Abs is the absolute value.
func Abs(r Rank) Rank { return Rank{"$abs": r} }

// Exp is e^r.
func Exp(r Rank) Rank { return Rank{"$exp": r} }

// Log is the natural logarithm.
func Log(r Rank) Rank { return Rank{"$log": r} }

// RRF fuses rankings with reciprocal rank fusion:
// score = -Σ 1/(k + rank_i). Each input should be a Knn leaf with
// ReturnRank set; k is commonly 60.
func RRF(k float64, ranks ...Rank) Rank {
	terms := make([]Rank, len(ranks))
	for i, r := range ranks {
		terms[i] = Div(Val(1), Sum(Val(k), r))
	}
	return Mul(Val(-1), Sum(terms...))
}

// Aggregate picks records within each group.
type Aggregate map[string]any

// MinK keeps the k records with the smallest values of keys in each group.
func MinK(k int, keys ...string) Aggregate {
	return Aggregate{"$min_k": map[string]any{"keys": keys, "k": k}}
}

// MaxK keeps the k records with the largest values of keys in each group.
func MaxK(k int, keys ...string) Aggregate {
	return Aggregate{"$max_k": map[string]any{"keys": keys, "k": k}}
}

// GroupBy groups ranked results by metadata keys.
type GroupBy struct {
	Keys      []string
	Aggregate Aggregate
}

// Search is one search request.
type Search struct {
	// Where filters records; it may use KeyID and KeyDocument.
	Where Where
	// Rank orders results. Without it, records come back in insertion order.
	Rank    Rank
	GroupBy *GroupBy
	Limit   int
	Offset  int
	// Select lists the fields to return: KeyDocument, KeyEmbedding,
	// KeyMetadata, KeyScore or individual metadata keys.
	Select []string
}

func (s Search) body() map[string]any {
	body := map[string]any{}
	if s.Where != nil {
		body["filter"] = s.Where
	}
	if s.Rank != nil {
		body["rank"] = s.Rank
	}
	if s.GroupBy != nil {
		body["group_by"] = map[string]any{"keys": s.GroupBy.Keys, "aggregate": s.GroupBy.Aggregate}
	}
	limit := map[string]any{"offset": s.Offset}
	if s.Limit > 0 {
		limit["limit"] = s.Limit
	}
	body["limit"] = limit
	sel := s.Select
	if sel == nil {
		sel = []string{}
	}
	body["select"] = map[string]any{"keys": sel}
	return body
}

// SearchResult holds one result list per search. Columns a search did not
// select are nil for that search.
type SearchResult struct {
	IDs        [][]string
	Documents  [][]*string
	Embeddings [][][]float32
	Metadatas  [][]Metadata
	Scores     [][]*float64
	// Select echoes the selected keys, sorted as the server returns them.
	Select [][]string
}

// Search runs one or more searches in a single request. The Search API is
// served by Kaleid and Chroma Cloud (not local Chroma).
func (col *Collection) Search(ctx context.Context, searches ...Search) (*SearchResult, error) {
	payloads := make([]map[string]any, len(searches))
	for i, s := range searches {
		payloads[i] = s.body()
	}
	var raw struct {
		IDs        [][]string       `json:"ids"`
		Documents  [][]*string      `json:"documents"`
		Embeddings [][]float32s     `json:"embeddings"`
		Metadatas  [][]Metadata     `json:"metadatas"`
		Scores     [][]*json.Number `json:"scores"`
		Select     [][]string       `json:"select"`
	}
	if err := col.c.do(ctx, http.MethodPost, col.path("/search"), map[string]any{"searches": payloads}, &raw); err != nil {
		return nil, err
	}
	out := &SearchResult{IDs: raw.IDs, Documents: raw.Documents, Metadatas: raw.Metadatas, Select: raw.Select}
	out.Embeddings = make([][][]float32, len(raw.Embeddings))
	for i, e := range raw.Embeddings {
		out.Embeddings[i] = toFloat32Matrix(e)
	}
	out.Scores = make([][]*float64, len(raw.Scores))
	for i, row := range raw.Scores {
		if row == nil {
			continue
		}
		out.Scores[i] = make([]*float64, len(row))
		for j, n := range row {
			if n != nil {
				f, err := n.Float64()
				if err != nil {
					return nil, err
				}
				out.Scores[i][j] = &f
			}
		}
	}
	return out, nil
}
