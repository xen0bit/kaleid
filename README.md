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

Your data lives in ordinary PostgreSQL tables, so you get Postgres backups,
replication, point-in-time recovery, monitoring and access control. Kaleid
itself is a stateless Go binary, so you can run as many replicas as you like.

## Quickstart

```sh
docker compose up -d        # PostgreSQL 17 + pgvector, Kaleid on :8000
curl localhost:8000/api/v2/heartbeat
```

Or run the binary against an existing database. The role needs permission to
create extensions and schemas, or `vector` and `pg_trgm` must already be
installed:

```sh
go build -o kaleid ./cmd/kaleid
KALEID_DATABASE_URL=postgres://user:pass@host:5432/db ./kaleid
```

Migrations run automatically on start, or on their own with `kaleid migrate`.

## Configuration

Every flag can also be set with the environment variable shown.

| Flag | Environment | Default | Description |
|---|---|---|---|
| `--database-url` | `KALEID_DATABASE_URL` | (required) | PostgreSQL connection URL |
| `--listen` | `KALEID_LISTEN` | `:8000` | Listen address |
| `--auth` | `KALEID_AUTH_PROVIDER` | `none` | `none` or `token` |
| `--auth-token` | `KALEID_AUTH_TOKEN` | | Single full-access token (token auth) |
| `--auth-tokens-file` | `KALEID_AUTH_TOKENS_FILE` | | JSON list of scoped tokens (see below) |
| `--allow-reset` | `KALEID_ALLOW_RESET` | `false` | Enable `POST /api/v2/reset` (deletes everything) |
| `--cors-allow-origins` | `KALEID_CORS_ALLOW_ORIGINS` | | Comma-separated origins, or `*` |
| `--max-batch-size` | `KALEID_MAX_BATCH_SIZE` | `5461` | Batch size advertised to clients |
| `--max-payload-bytes` | `KALEID_MAX_PAYLOAD_BYTES` | `41943040` | Maximum request body size |
| `--index-threshold` | `KALEID_INDEX_THRESHOLD` | `10000` | Records before a collection's HNSW index is built (`0` = build immediately) |
| `--index-build-memory` | `KALEID_INDEX_BUILD_MEMORY` | `256MB` | `maintenance_work_mem` for index builds |
| `--exact-fallback` | `KALEID_EXACT_FALLBACK` | `true` | Rerun filtered queries that come back short as exact scans |
| `--max-scan-tuples` | `KALEID_MAX_SCAN_TUPLES` | `20000` | `hnsw.max_scan_tuples` for iterative index scans |
| `--log-level` | `KALEID_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |

### Authentication

Open-source Chroma performs no server-side auth, so the default is `none`.
With `--auth token`, requests need either `Authorization: Bearer <token>` or
`X-Chroma-Token: <token>`, the two headers Chroma's clients send. A token file
limits each token to a tenant and a set of databases. If you omit `tenant` or
`databases`, the token gets full access:

```json
[
  {"token": "s3cret-admin"},
  {"token": "team-a-token", "user_id": "team-a", "tenant": "default_tenant", "databases": ["team_a"]}
]
```

`GET /api/v2/auth/identity` reports the tenant and databases of the caller's
token, which clients use to choose their defaults.

## Compatibility

The target is **Chroma 1.5.9**. For every request, Kaleid returns the same
status code, JSON shape and error type as Chroma 1.5.9, and nearly always the
same error message. This includes Chroma behaviours that are easy to get wrong:

- Adding an existing id is silently ignored.
- `update` and `upsert` merge metadata, and a `null` value deletes a key.
- `get` returns records in insertion order.
- `$ne` and `$nin` match records that don't have the key.
- Numbers compare across int and float, but a bool never equals a number.
- A float stored as `3.0` comes back as `3.0`, so Python reads it as a float.
- `l2` distances are squared and `ip` distances are `1 - dot`.
- Collection `configuration_json` and `schema` include every default, and
  `schema` gains a key entry for each new metadata key and type.

| Feature | Status |
|---|---|
| Tenants, databases, collections (CRUD, `get_or_create`, rename, metadata, configuration, schema) | Supported |
| `add` / `upsert` / `update` / `delete` / `get` / `count` / `query`, including base64 embeddings | Supported |
| `where` and `where_document` filters (`$eq` `$ne` `$gt` `$gte` `$lt` `$lte` `$in` `$nin` `$contains` `$not_contains` `$regex` `$not_regex` `$and` `$or`, array metadata) | Supported |
| Legacy `hnsw:*` metadata, HNSW configuration, embedding-function configuration | Supported |
| Search API (`/search`): KNN rank expressions, RRF, arithmetic, `group_by`, `select`, pagination | Supported. In Chroma this is Cloud-only. |
| Sparse vectors and BM25 (server-side IDF) | Supported |
| Collection forking, `fork_count`, `indexing_status`, lookup by CRN | Supported. In Chroma these are Cloud-only. |
| Token auth | Supported (Kaleid addition) |
| Attached functions, Sync, CMEK | Not supported (501) |
| In-process clients (`PersistentClient`, `EphemeralClient`) | Not applicable: use `HttpClient` |

Where Kaleid differs on purpose:

- **Forks are copies.** Chroma Cloud forks are copy-on-write. Kaleid copies
  the data inside one transaction, so a fork is isolated immediately but costs
  time and storage in proportion to the collection's size.
- **Numeric comparisons follow the math.** Local Chroma truncates a float
  operand when it compares it against an integer, so `{"x": {"$gte": 2.5}}`
  matches `x = 2`. Kaleid compares values exactly.
- **Some error messages differ.** Regex syntax errors, and errors that Chroma
  builds from Rust `Debug` output, have different text. The status code and
  error type still match.
- **Reads are always consistent.** `read_level` is accepted and ignored,
  because every committed write is visible to the next read.

## How it works

- **Catalog.** Tenants, databases and collections live in the `kaleid` schema.
  A collection's `schema` is the source of truth, and `configuration_json` is
  derived from it, the same way Chroma does it.
- **One table per collection.** Each collection's records live in
  `kaleid_data.c_<id>` with a typed `vector(d)` column. `d` is set by the first
  write, as in Chroma. Each table also has a GIN `jsonb_path_ops` index on
  metadata and a GIN trigram index on documents. Deleting a collection drops
  its table.
- **Vector index.**
  - Collections smaller than `--index-threshold` are searched with exact
    scans, which are fast at that size and perfectly accurate.
  - At the threshold, Kaleid builds an HNSW index in one bulk pass, using the
    collection's space as the operator class and its `max_neighbors` and
    `ef_construction` as build parameters.
  - Embeddings of 2,001–4,000 dimensions are indexed as `halfvec` and
    re-ranked at full precision. Above 4,000 dimensions, searches are exact.
- **Filtered queries.** These use pgvector's iterative index scans
  (`strict_order`). If the index still returns fewer than `n_results` rows
  while more records match the filter, Kaleid reruns the query as an exact
  scan, so a query always returns `n_results` whenever that many records match.
- **Filters.** `where` clauses compile to type-aware JSONB SQL.
  `$contains` compiles to a trigram-indexed, case-sensitive `LIKE`. `$regex`
  patterns are parsed as RE2/Rust regex syntax and re-emitted as PostgreSQL
  regular expressions from the syntax tree, so the two regex dialects can't
  drift apart.
- **Sparse vectors** are also written to a per-collection postings table. That
  table serves sparse KNN (`1 - q·d`) and supplies the document frequencies for
  BM25 IDF.
- **Writes** hold a per-collection advisory lock. Each batch is applied in
  request order with Chroma's log semantics and written in bulk with `unnest`.

## Performance notes

Measured on one workstation (PostgreSQL 17 in Docker, Chroma 1.5.9 `chroma run`),
with 20,000 random 384-dimensional vectors, cosine distance, `n_results=10`, and
the Python client:

| | Ingest | Query | Filtered query (1% selectivity) | Recall@10 |
|---|---|---|---|---|
| Chroma 1.5.9 | 4,560/s | 4.7 ms | 12.5 ms | 0.34 |
| Kaleid, HNSW built at 10k (default) | 818/s | 5.3 ms | 3.5 ms | 0.40 |
| Kaleid, no index (exact scan) | 12,800/s | 9.1 ms | 2.3 ms | 1.00 |

pgvector maintains its HNSW graph one row at a time. Once a collection's index
exists, that per-row maintenance dominates ingest cost. For large bulk loads,
raise `--index-threshold` so the index is built in one pass after the data is
loaded. Filtered queries are fast because the metadata filter uses a GIN index,
and pgvector's iterative scans stop as soon as they have enough matches.

## Development

```sh
docker compose up -d postgres
go test ./internal/...                                                  # unit tests
KALEID_TEST_DATABASE_URL=postgres://kaleid:kaleid@localhost:5432/kaleid?sslmode=disable \
  go test ./test/integration/                                           # Kaleid end-to-end
CHROMA_URL=http://localhost:8001 KALEID_URL=http://localhost:8000 \
  go test ./test/compat/                                                # differential vs real Chroma
```

CI runs three kinds of compatibility check:

1. **Differential tests** (`test/compat`) replay each scenario against the
   official `chromadb/chroma:1.5.9` image and against Kaleid, then compare the
   normalized responses.
2. **Chroma's own test suite** at the 1.5.9 tag, including the Hypothesis
   property tests with their in-memory reference model. CI points it at Kaleid
   through Chroma's integration-test mode (`CHROMA_INTEGRATION_TEST_ONLY=1`).
   `test/upstream/run.sh` runs the same suite locally.
3. **Integration tests** (`test/integration`) cover the features that local
   Chroma can't check: search, sparse/BM25, fork, auth and recall at high
   dimensions.

## License

MIT. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
