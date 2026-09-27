# Clients

Kaleid implements Chroma's v2 HTTP API, so you connect with Chroma's
clients. Only the address changes. Kaleid also has a Go client of its own.

| Language | Package | Connect |
|---|---|---|
| Python | `chromadb-client` (HTTP only) or `chromadb` | `chromadb.HttpClient(host="kaleid.example.com", port=8000)` |
| JavaScript / TypeScript | `chromadb` | `new ChromaClient({ host: "kaleid.example.com", port: 8000 })` |
| Rust | `chroma` | Point it at the server URL, as you would for a Chroma server |
| Go | `github.com/xen0bit/kaleid/pkg/client` | `client.New("http://kaleid.example.com:8000")` |

Compatibility is tested with the Python client: Chroma's own test suite runs
against Kaleid in CI. The JS and Rust clients use the same API.

Chroma's in-process clients (`PersistentClient`, `EphemeralClient`) run a
database inside your program and never talk to a server, so they can't use
Kaleid.

## Authentication

If the server has [token auth](configuration.md#authentication) enabled, send
the token in either header Chroma's clients support:

```python
from chromadb.config import Settings

client = chromadb.HttpClient(
    host="kaleid.example.com", port=8000,
    settings=Settings(chroma_client_auth_provider="token",
                      chroma_client_auth_credentials="s3cret"),
)
# Sends "Authorization: Bearer s3cret". To send X-Chroma-Token instead, add
# chroma_auth_token_transport_header="X-Chroma-Token" to the settings.
```

```go
c := client.New(url, client.WithToken("s3cret"))       // Authorization: Bearer
c := client.New(url, client.WithChromaToken("s3cret"))  // X-Chroma-Token
```

## Tenants and databases

Everything lives in a tenant and a database, which default to
`default_tenant` and `default_database`. In Python, pass `tenant=` and
`database=` to `HttpClient`. In Go:

```go
c := client.New(url, client.WithTenant("acme"), client.WithDatabase("prod"))
other := c.Scoped("acme", "staging") // a copy of c for another database
```

## The Go client

`pkg/client` has no dependencies beyond the standard library. It covers
tenants, databases, collections, records, search, fork and indexing status.

```go
col, err := c.CreateCollection(ctx, "articles", &client.CreateCollectionOptions{
	HNSW:        &client.HNSWConfig{Space: client.SpaceCosine},
	Metadata:    client.Metadata{"owner": "search-team"},
	GetOrCreate: true,
})

err = col.Upsert(ctx, client.Records{
	IDs:        []string{"a1"},
	Embeddings: [][]float32{vec},
	Documents:  []string{"..."},
	Metadatas:  []client.Metadata{{"year": 2026, "score": 4.5, "tags": []string{"go", "db"}}},
})

res, err := col.Get(ctx, client.GetOptions{
	Where:   client.Where{"tags": client.Where{"$contains": "go"}},
	Include: []client.Include{client.IncludeDocuments, client.IncludeMetadatas},
})

n, err := col.Delete(ctx, client.DeleteOptions{Where: client.Where{"year": client.Where{"$lt": 2020}}})
```

Some details:

- **Metadata types.** Values can be `bool`, `string`, any integer type,
  `float32`/`float64`, slices of these, or `client.SparseVector`. They come
  back as `bool`, `string`, `int64`, `float64`, slices, or
  `*client.SparseVector`.
- **Ints and floats stay distinct.** The client sends `2.0` as a float, so it
  comes back as `float64`. Plain `encoding/json` would turn it into an int.
- **Deleting keys.** In `Update` and `Upsert`, a `nil` metadata value removes
  that key.
- **Errors.** Failed requests return `*client.Error`, which carries the
  status code, the server's error name and its message. Check it with
  `client.IsNotFound`, `client.IsConflict` or `client.IsInvalidArgument`.

The same client works against Chroma. CI runs one client test flow against
both servers and requires identical results.

## Embeddings

Chroma's Python and JS clients embed text for you, using an embedding
function. With the Go client you embed text yourself and pass vectors in.
`pkg/embed` provides embedders:

| Embedder | Use |
|---|---|
| `embed.NewOpenAI(key, "text-embedding-3-small")` | OpenAI |
| `embed.NewOllama("http://localhost:11434", "nomic-embed-text")` | A local Ollama server |
| `&embed.OpenAICompatible{BaseURL: ..., Model: ...}` | Any OpenAI-compatible embeddings API (vLLM, LM Studio, TEI, gateways) |
| `embed.NewHashing(384)` | Offline and deterministic, with no model. For tests and demos only: it matches words, not meaning. |
| `embed.NewBM25()` | Sparse BM25 vectors for keyword search; see [Search API](search.md#bm25-and-hybrid-search) |

`embed.FromEnv()` picks an embedder from the `EMBEDDER` environment variable,
as the examples do.

Always query a collection with the same embedder that filled it. When you
create a collection, the Python client stores its embedding-function
configuration so that other Python clients can recreate it. Kaleid stores
this configuration and returns it unchanged. The Go client passes it through
as `CreateCollectionOptions.EmbeddingFunction`.
