# Search API

`query` finds the nearest neighbours of one embedding. The Search API
(`POST .../collections/{id}/search`) is more general. A search can:

- combine several rankings with arithmetic, including reciprocal rank fusion
  (RRF);
- rank by sparse vectors, such as BM25;
- group results;
- choose which fields come back.

In Chroma, the Search API is available only in Chroma Cloud. Kaleid serves it
on every deployment, with the same request format, so Chroma's Python and JS
`Search` builders work unchanged.

## A search request

A request holds one or more searches, which run independently:

| Part | Meaning |
|---|---|
| `filter` | A `where` clause. It can use `#id` and `#document` as well as metadata keys, and it applies before ranking. |
| `rank` | A rank expression. **Lower scores rank first.** Without one, records come back in insertion order and have no score. |
| `group_by` | Groups records by metadata keys and keeps the top `k` in each group. |
| `limit` | `{"limit": n, "offset": m}` for pagination. |
| `select` | The fields to return: `#document`, `#embedding`, `#metadata`, `#score`, or individual metadata keys. |

In Go:

```go
res, err := col.Search(ctx, client.Search{
	Where:  client.Where{"lang": "en"},
	Rank:   client.Knn(queryVec),
	Limit:  10,
	Select: []string{client.KeyDocument, client.KeyScore, "title"},
})
// res.IDs[0], res.Documents[0], res.Scores[0], res.Metadatas[0] (only "title")
```

## Rank expressions

| Expression | Go | Meaning |
|---|---|---|
| `{"$knn": {...}}` | `client.Knn(vec, opts)` / `client.KnnSparse(sv, key, opts)` | Nearest neighbours: distance for dense vectors, `1 - q·d` for sparse |
| `{"$val": x}` | `client.Val(x)` | A constant |
| `$sum`, `$mul`, `$max`, `$min` | `client.Sum(...)`, `Mul`, `Max`, `Min` | Combine two or more expressions |
| `$sub`, `$div` | `client.Sub(a, b)`, `client.Div(a, b)` | Subtract or divide |
| `$abs`, `$exp`, `$log` | `client.Abs(r)`, `Exp`, `Log` | Apply a function |

A `$knn` leaf takes these options:

| Option | Meaning |
|---|---|
| `limit` | How many nearest records it contributes (default 16) |
| `key` | `#embedding` for dense vectors, or a metadata key that holds sparse vectors |
| `return_rank` | Score by position (0, 1, 2, ...) instead of by distance |
| `default` | The score for records outside this leaf's results |

The rule that surprises people: **when combining leaves, a record that one
leaf didn't return is dropped unless that leaf has a `default`.** Without
defaults, a sum of two KNN leaves keeps only the records both returned.

## Reciprocal rank fusion

RRF scores a record by `-Σ 1/(k + rank_i)` across rankings. Clients build it
from the primitives above; there is no dedicated operator. Give each leaf a
`default` equal to its limit, so that a record missing from one ranking
counts as ranked last there instead of being dropped:

```go
last := 100.0
rank := client.RRF(60,
	client.Knn(denseVec, client.KnnOptions{Limit: 100, ReturnRank: true, Default: &last}),
	client.KnnSparse(bm25Vec, "bm25", client.KnnOptions{Limit: 100, ReturnRank: true, Default: &last}),
)
```

In Python the same fusion is `Rrf([Knn(query=..., return_rank=True, default=...), ...], k=60)`.

## BM25 and hybrid search

BM25 has two halves:

1. **Documents.** Each document stores a sparse vector of saturated term
   frequencies in a metadata key. Kaleid indexes sparse vectors wherever they
   appear in metadata.
2. **Queries.** Mark that key's sparse index `"bm25": true` in the collection
   schema. At search time, Kaleid then multiplies each query term's weight by
   its inverse document frequency, `ln((N − n_t + 0.5) / (n_t + 0.5) + 1)`,
   computed over the whole collection. This matches Chroma Cloud.

In Go, `pkg/embed` covers both halves:

```go
bm25 := embed.NewBM25()
col, _ := c.CreateCollection(ctx, "docs", &client.CreateCollectionOptions{
	Schema: embed.BM25Schema("bm25"), // sparse index on key "bm25" with bm25: true
})
_ = col.Add(ctx, client.Records{
	IDs: ids, Embeddings: denseVecs, Documents: texts,
	Metadatas: []client.Metadata{{"bm25": bm25.EncodeDocument(texts[0])}, ...},
})
hits, _ := col.Search(ctx, client.Search{Rank: client.KnnSparse(bm25.EncodeQuery("replication backups"), "bm25"), Limit: 10})
```

Chroma's Python and JS clients produce these vectors automatically when a
collection's schema uses their BM25 sparse embedding function. `embed.BM25`
tokenizes differently (it doesn't stem words), so don't mix vectors from the
two in one collection.

A complete, runnable version is in
[`examples/hybrid_search`](../examples/hybrid_search).

## Grouping

Grouping keeps the best `k` records per value of one or more metadata keys,
then re-sorts all kept records by score:

```go
client.Search{
	Rank:    rank,
	GroupBy: &client.GroupBy{Keys: []string{"product"}, Aggregate: client.MinK(1, client.KeyScore)},
}
```

`MaxK` keeps the largest values instead. A `group_by` needs a `rank`.
