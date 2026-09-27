# Kaleid

Kaleid is a Chroma-compatible vector database server backed by PostgreSQL and
[pgvector](https://github.com/pgvector/pgvector). It speaks the
[Chroma](https://github.com/chroma-core/chroma) v2 HTTP API, so the official
Chroma clients work against it unchanged:

```python
import chromadb

client = chromadb.HttpClient(host="localhost", port=8000)
collection = client.get_or_create_collection("docs")
collection.add(ids=["a", "b"], documents=["hello world", "goodbye world"])
print(collection.query(query_texts=["hi"], n_results=1))
```

What you get by running it on Postgres:

- **Standard Postgres operations.** Your data lives in ordinary PostgreSQL
  tables, so backups, replication, point-in-time recovery, monitoring and
  access control all work the usual way.
- **Easy scaling.** Kaleid itself is a stateless Go binary, so you can run as
  many replicas as you like.
- **Cloud-only Chroma features everywhere.** Kaleid also serves features that
  Chroma offers only in Chroma Cloud: the hybrid Search API, BM25 and sparse
  vectors, and collection forking.

## Quickstart

```sh
docker compose up -d        # PostgreSQL 17 + pgvector, and Kaleid on :8000
curl localhost:8000/api/v2/heartbeat
```

To use an existing database instead, see
[Deployment](docs/deployment.md#postgresql), then run:

```sh
go build -o kaleid ./cmd/kaleid
KALEID_DATABASE_URL=postgres://user:pass@host:5432/db ./kaleid
```

## Go client

Chroma publishes clients for Python, JS and Rust, and all of them work
against Kaleid. For Go, there's [`pkg/client`](pkg/client), which has no
dependencies beyond the standard library. [`pkg/embed`](pkg/embed) provides
embedders to go with it: OpenAI, Ollama, any OpenAI-compatible API, BM25, and
an offline one for tests.

```go
c := client.New("http://localhost:8000")
col, err := c.CreateCollection(ctx, "docs", &client.CreateCollectionOptions{GetOrCreate: true})
err = col.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: docs})
res, err := col.Query(ctx, client.QueryOptions{Embeddings: [][]float32{q}, NResults: 5,
	Where: client.Where{"year": client.Where{"$gte": 2025}}})
```

## Examples and sample apps

Go ports of Chroma's examples and sample apps. All of them run against Kaleid
in CI.

- **[examples/](examples):**
  - `start_here`: basic retrieval;
  - `where_filtering`: metadata and document filters;
  - `embeddings`: different embedding providers;
  - `auth`: token authentication;
  - `forking`: branching a collection;
  - `hybrid_search`: dense and BM25 search fused with RRF;
  - `chat_with_your_documents`: retrieval-augmented chat with OpenAI, Ollama,
    Gemini or xAI;
  - a systemd deployment example.
- **[sample_apps/](sample_apps):**
  - `movies`: a web app with hybrid search and an LLM chat that searches as a
    tool;
  - `generative_benchmarking`: compare embedding models on a benchmark built
    from your own documents.

## Documentation

| | |
|---|---|
| [Getting started](docs/getting-started.md) | First run, first query |
| [Clients](docs/clients.md) | Python, JS, Rust and Go, plus embeddings and auth |
| [Search API](docs/search.md) | Hybrid search, RRF, BM25, grouping |
| [Configuration](docs/configuration.md) | Flags, environment variables, token auth |
| [Deployment](docs/deployment.md) | PostgreSQL requirements, Docker, systemd, scaling |
| [Operations](docs/operations.md) | Health, backups, index tuning, maintenance, upgrades |
| [Compatibility](docs/compatibility.md) | What matches Chroma 1.5.9, what differs, how it's tested |
| [Architecture](docs/architecture.md) | How it maps onto PostgreSQL |

## Compatibility

The target is **Chroma 1.5.9**. Kaleid returns the same status codes, JSON
shapes and error classes. It also reproduces the Chroma behaviours that are
easy to get wrong, for example:

- adding an existing id is ignored;
- `update` merges metadata;
- `$ne` matches records that lack the key;
- `l2` distances are squared;
- a float stored as `3.0` comes back as `3.0`.

Every pull request is checked three ways:

- Chroma's own test suite runs against Kaleid.
- Differential tests send the same requests to the official Chroma image and
  to Kaleid and compare the responses.
- Integration tests cover the features local Chroma lacks.

[docs/compatibility.md](docs/compatibility.md) lists the details and the few
deliberate differences.

## Performance

These numbers come from one workstation: PostgreSQL 17 in Docker against
Chroma 1.5.9 (`chroma run`), 20,000 random 384-dimensional vectors, cosine
distance, `n_results=10`, using the Python client.

| | Ingest | Query | Filtered query (1% selectivity) | Recall@10 |
|---|---|---|---|---|
| Chroma 1.5.9 | 4,560/s | 4.7 ms | 12.5 ms | 0.34 |
| Kaleid, HNSW built at 10k records (default) | 818/s | 5.3 ms | 3.5 ms | 0.40 |
| Kaleid, no index (exact scan) | 12,800/s | 9.1 ms | 2.3 ms | 1.00 |

- **Ingest after indexing.** pgvector maintains its HNSW graph one row at a
  time, so ingest into an already-indexed collection is the slow case. For
  bulk loads, raise `--index-threshold` so the index is built in one pass
  after loading; see [Operations](docs/operations.md#vector-indexes).
- **Filtered queries** are fast because the metadata filter uses a GIN index,
  and pgvector's iterative scans stop as soon as they have enough matches.

## Development

```sh
docker compose up -d postgres
go test ./internal/... ./pkg/...                                    # unit tests
KALEID_TEST_DATABASE_URL=postgres://kaleid:kaleid@localhost:5432/kaleid?sslmode=disable \
  go test ./test/integration/                                       # end-to-end against Kaleid
CHROMA_URL=http://localhost:8001 KALEID_URL=http://localhost:8000 \
  go test ./test/compat/                                            # differential tests against real Chroma
KALEID_HOST=localhost KALEID_PORT=8000 test/upstream/run.sh chromadb/test/property/test_filtering.py
```

The upstream runner needs a Kaleid started with `KALEID_ALLOW_RESET=true`.

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
