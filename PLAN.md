# Kaleid: implementation plan

Section 12 records the scoping decisions made on 2026-09-26.

Kaleid is a Go server that speaks the Chroma HTTP API (v2) and stores its data in PostgreSQL with pgvector. The goal is that the official clients work against Kaleid unchanged: Python `chromadb` / `chromadb-client`, JS `chromadb`, and the Rust client. Pointing `chromadb.HttpClient(host=...)` at Kaleid should behave the same as pointing it at Chroma.

## 0. Research basis

Everything below comes from these sources, not from memory:

- **Chroma source.** `chroma-core/chroma` at `e122af7` (2026-09-24). The HTTP server is the Rust `frontend` crate (`rust/frontend/src/server.rs`, `rust/frontend-core/src/routes.rs`). Semantics come from `rust/types` (`where_parsing.rs`, `validators.rs`, `execution/operator.rs`, `collection_schema.rs`) and `rust/error`.
- **Live reference server.** `chromadb==1.5.9` (latest release) run locally:
  - `/openapi.json` was captured: 36 operations, 91 schemas.
  - About 80 requests probed edge cases. Section 3 summarizes the results.
- **Python client.** Covers error mapping, base64 packing, and client-side validation and embedding.
- **pgvector v0.8.6.** Types, index limits, operators, and iterative scans.
- **Chroma docs.** In-repo at `docs/mintlify`. Used for the OSS vs Cloud feature split, conditional transactions, and forking.

## 1. Compatibility target

**Definition of "API compatible":** an official client cannot tell Kaleid from Chroma. That covers:

- Same routes and methods.
- Same JSON request and response shapes, including `null` placement and default `include` lists.
- Same validation outcomes.
- Same HTTP status codes plus `error` / `message` bodies. The clients map `error` names to exception classes, and some code paths match on message text.
- Same distance values.
- Same defaults echoed in `configuration_json` and `schema`.

**Pinned target: Chroma 1.5.9.** Master already has routes that 1.5.9 lacks:

- `POST .../collections/{id}/conditional/get`
- `POST .../collections/{id}/conditional/commit`
- `GET .../databases/by-id/{database_id}`

The design should leave room for these, but they don't ship until Chroma releases them.

### Client modes

| Client mode | Works with Kaleid? |
|---|---|
| `HttpClient` / `AsyncHttpClient` (Python) | Yes. This is the primary target. |
| JS `ChromaClient` / Rust client | Yes. Same REST API. |
| `CloudClient` | Probably, via `cloud_host`/`cloud_port` overrides plus API-key header handling. Needs verification. |
| `PersistentClient` / `EphemeralClient` | **No.** These are in-process Rust bindings with nothing on the wire. Out of scope. |

## 2. API surface and scope tiers

Tier A is the OSS parity target (what local Chroma serves). Tier B covers features that only Chroma Cloud serves; OSS 1.5.9 returns 501/500 for them. Tier C is out of scope.

| Endpoint | Tier | Notes |
|---|---|---|
| `GET /api/v2`, `/heartbeat` | A | `{"nanosecond heartbeat": <u64>}` |
| `GET /healthcheck`, `/version` | A | `/version` returns `"1.0.0"` (JSON string). |
| `GET /pre-flight-checks` | A | `{"max_batch_size":5461,"supports_base64_encoding":true}`. Clients split batches by this value. |
| `GET /auth/identity` | A | `{"user_id":"","tenant":"default_tenant","databases":["default_database"]}` |
| `POST /reset` | A | Gated by config (`allow_reset`). |
| Tenants: `POST /tenants`, `GET` and `PATCH /tenants/{t}` | A | Name ≥3 chars. `resource_name` is used by PATCH. |
| Databases: list, create, get, delete | A | The default DB has the all-zero UUID. |
| Collections: list (`limit`/`offset`), count, create (`get_or_create`), get by name, get by id, update (PUT), delete | A | See configuration/schema (§5.3). |
| Records: `add`, `upsert`, `update`, `delete`, `get`, `count`, `query` | A | The core of the product. |
| `GET /collections/{crn}` | B | CRN = `tenant:database:collection`. Cheap to add. |
| `GET .../indexing_status` | B | Always report fully indexed (PG is synchronous). |
| `POST .../fork`, `GET .../fork_count` | B | Copy-based in PG. Not copy-on-write. |
| `POST .../search` | B | Rank expressions, RRF, `group_by`, `select`, sparse/BM25, `read_level`. **Largest Tier-B item.** |
| `conditional/get`, `conditional/commit` | B (post-1.5.9) | A natural fit for PG optimistic concurrency. |
| `functions/attach`, `attached_functions/*` | C | Cloud async compute. Return 501. |
| Sync service, CMEK, quotas/metering, package search | C | Out of scope. |
| `/api/v1/*` | A | Deprecation notice (mirror Chroma's response). |
| `/openapi.json`, `/docs` | A | Serve Kaleid's own spec, generated from the same Go types. |

## 3. Observed behavior that must be replicated

All of these were verified against the 1.5.9 reference server. Most are easy to get wrong.

**Writes**

- `add` with an existing id is **silently ignored** (201 `{}`), and the original record is kept. Duplicate ids within one batch keep the first.
- `update` of a nonexistent id is silently ignored (200 `{}`).
- `update`/`upsert` **merge** metadata into the existing metadata, and a `null` value **deletes** that key.
- In `update`, a `null` document means "unchanged".
- Collection `PUT` with `new_metadata` **replaces** collection metadata wholesale.
- `get_or_create` with metadata on an existing collection ignores the new metadata.
- `add`/`upsert` require `embeddings`. Clients embed before sending, so the server never embeds. Sending `embeddings: null` gives **422** `ChromaError` "Failed to deserialize the JSON body…".
- Embeddings arrive either as float arrays or as base64 strings of **little-endian f32** (for example `AACAPgAAQD8=` → `[0.25, 0.75]`).
- Collection `dimension` is `null` until the first write, then fixed. On mismatch: `400 InvalidArgumentError "Collection expecting embedding with dimension of 3, got 2"`. This applies to queries too.
- Length mismatch: `400 "Inconsistent number of IDs, embeddings, documents, URIs and metadatas"`.
- An empty id gives `"Empty ID, ID must have at least one character"`.
- Metadata keys may not start with `#` or `$` (`"Invalid metadata at index N"`). Empty `{}` metadata is accepted server-side; only the Python client rejects it.
- `delete` returns `{"deleted": n}`. `delete {}` deletes nothing.

**Reads**

- `get` returns records in **insertion order**, even when `ids` are given in a different order.
- Default `include`:
  - `get`: `["documents","metadatas"]`
  - `query`: `["documents","metadatas","distances"]`
- Fields not included are `null`, and the `include` list is echoed back.
- `query` shapes are nested per query embedding. `n_results > count` returns what exists. `n_results: 0` returns `[[]]` shapes.

**Distances** (these differ from pgvector's raw operators)

- `l2` = **squared** Euclidean. pgvector `<->` is not squared.
- `cosine` = `1 − cos_sim`. Matches pgvector `<=>`.
- `ip` = `1 − dot`. pgvector `<#>` is `−dot`.

**Filters**

- A top-level `where` must have exactly one key. `{"a":1,"b":2}` and `{}` are rejected ("Invalid where clause"); callers must use `$and`.
- Numbers compare across int and float (`3` matches stored `3.0`). Bool never equals int (`{"flag":1}` does not match `true`).
- Range operators (`$gt`, `$gte`, `$lt`, `$lte`) are numeric-only; a string operand is rejected.
- Missing-key semantics:
  - `$ne` and `$nin` **match records that lack the key**.
  - `$eq` and `$in` do not.
- Mixed-type `$in` is accepted server-side (the Python client rejects it).
- `$contains` on an array-valued metadata field means element membership.
- `$regex` / `$not_regex` are only valid on `#document`.
- `where` also accepts the `#document` key (`{"#document":{"$contains":…}}`).
- `where_document` supports `$contains`, `$not_contains`, `$regex`, `$not_regex`, `$and`, and `$or`. Contains is a **case-sensitive substring** match (local Chroma uses a trigram FTS index plus an exact check).

**Collections**

- Name rules: 3–512 characters from `[a-zA-Z0-9._-]`, starting and ending alphanumeric, no `..`, and not an IPv4 address. The error message is verbatim.
- The `configuration_json` echo includes defaults: `hnsw {space, ef_construction:100, ef_search:100, max_neighbors:16, resize_factor:1.2, sync_threshold:1000}`, `spann:null`, `embedding_function:null`.
- Legacy `metadata["hnsw:space"]` is honored.
- The `schema` echo includes the full default index tree (`#document` FTS on, `#embedding` vector index with `source_key:"#document"`, inverted indexes on by default).
- The schema **grows a `keys` entry for every metadata key and value type written** (for example `"k": {int…, float…}`).
- Clients read `configuration_json.embedding_function` and `schema` to rebuild embedding functions, so these must round-trip faithfully.

**Errors**

- Body shape: `{"error": <name>, "message": <text>}`.

| Status | `error` | Example message |
|---|---|---|
| 400 | `InvalidArgumentError` | Validation failures |
| 404 | `NotFoundError` | `"Collection [x] does not exist"`, `"Tenant [x] not found"` |
| 409 | `ChromaError` | `"Collection [x] already exists"`, `"Database [x] already exists"` |
| 422 | `ChromaError` | JSON deserialize failures |
| 501 | `ChromaError` | Unsupported features |
| 500 | `InternalError` | Internal failures |

- Unknown routes return 404 with an empty body.
- Responses may carry a `chroma-trace-id` header.

**Output formatting**

- A float metadata value stored as `3.0` must serialize as `3.0`, not `3`. Otherwise Python reads it back as `int`.
- Embeddings serialize with float32 shortest representation.
- Metadata key order is irrelevant (Chroma's is unordered).

**Auth**

- OSS Chroma 1.x has **no server-side auth**. The only `AuthenticateAndAuthorize` impl is a no-op `()`.
- Clients send `Authorization: Bearer …` or `X-Chroma-Token` (and Cloud sends `x-chroma-token` API keys).

## 4. Architecture

```
            ┌─────────────── kaleid (single Go binary, stateless, N replicas) ───────────────┐
 clients ──▶│ chi router → middleware (trace-id, JSON errors, body limit, CORS, auth)        │
            │   → handlers (api/) → service layer (validation, semantics)                    │
            │       → filter compiler (where/where_document → SQL)                           │
            │       → rank compiler (search rank tree → SQL CTEs)                            │
            │       → store (pgx pool, DDL manager, per-collection tables)                   │
            └────────────────────────────────────────────┬───────────────────────────────────┘
                                                         ▼
                             PostgreSQL 17 + pgvector ≥0.8 + pg_trgm
```

### Go stack

- **Go 1.27** (installed).
- **Router: `go-chi/chi/v5`.** Go's stdlib `ServeMux` **panics** on these two overlapping patterns, because neither is more specific:
  - `/databases/by-id/{id}`
  - `/databases/{db}/collections`

  chi's static-segment priority resolves this.
- **Database:** `jackc/pgx/v5` + `pgxpool`, and `pgvector/pgvector-go`.
- **Queries:** hand-written SQL plus a small builder. Filters are dynamic, so sqlc only fits the static catalog queries (optional there).
- **Migrations:** `pressly/goose` with embedded SQL, catalog only. Per-collection DDL is issued by the DDL manager.
- **Observability:** `log/slog`, `go.opentelemetry.io/otel` (traces), and Prometheus metrics.
- **JSON:** stdlib `encoding/json` with custom types. The precision-sensitive paths need custom marshalers: `json.Number` input to keep int vs float, float formatting that keeps `.0`, and f32 embeddings.
- **Tests:** `testcontainers-go` for integration tests against `pgvector/pgvector:pg17`.

### Layout

```
cmd/kaleid/            main: serve, migrate, version
internal/api/          routes, handlers, wire types (1:1 with Chroma OpenAPI), error mapping
internal/wire/         custom JSON: EmbeddingsPayload, metadata values, SparseVector, floats
internal/validate/     name, metadata, where, schema, configuration validation (verbatim messages)
internal/filter/       where/where_document AST + SQL compiler + Go-side evaluator (for tests)
internal/rank/         Search rank-expression AST + SQL compiler, group_by, select
internal/collection/   configuration/schema defaults, legacy hnsw:* mapping, schema key growth
internal/store/        pgx repo: catalog, records, DDL manager, index lifecycle
internal/auth/         none | token, identity resolution, authz
internal/config/       env + YAML
migrations/            catalog schema
test/compat/           differential harness vs reference Chroma
```

## 5. Postgres data model

### 5.1 Catalog (static migrations)

```sql
tenants(name text pk, resource_name text, created_at)
databases(id uuid pk, tenant text fk, name text, unique(tenant,name))
collections(id uuid pk, database_id uuid fk, name text, unique(database_id,name),
            metadata jsonb, configuration jsonb, schema jsonb,
            dimension int null, data_table text, log_position bigint default 0,
            version int default 0, forked_from uuid null, created_at)
```

Seed `default_tenant` / `default_database` (id `00000000-…`).

### 5.2 Record storage: one table per collection (recommended)

```sql
CREATE TABLE kaleid_data.c_<uuidhex> (
  rowid      bigserial PRIMARY KEY,         -- insertion order (get ordering)
  id         text NOT NULL UNIQUE,          -- Chroma record id
  embedding  vector(<dim>),                 -- typed once dimension known (created lazily on first write)
  document   text,
  uri        text,
  metadata   jsonb,
  seq        bigint NOT NULL                -- collection log_position at last write (conditional txns, indexing_status)
);
-- indexes
HNSW  (embedding vector_<space>_ops) WITH (m = max_neighbors, ef_construction)
GIN   (document gin_trgm_ops)                 -- $contains / $regex acceleration
GIN   (metadata jsonb_path_ops)               -- $eq / $in / array $contains via @>
```

**Why per-collection tables, and not one shared table?**

- pgvector HNSW needs a fixed dimension per indexed column, and each Chroma collection has its own dimension and distance space.
- A shared table would need one partial expression index per collection. Planning cost grows with the number of indexes, and deleting a collection would become a huge `DELETE` plus vacuum.
- With per-collection tables:
  - Each table gets a typed column and one index with the right opclass.
  - Delete is `DROP TABLE`.
  - Scans are naturally scoped to one collection.
  - Queries name the table directly, so there is no partition-pruning cost.

**Cost:** catalog growth. This is fine up to roughly tens of thousands of collections. That covers the target workload of up to about 100k collections, so a shared-table mode is not planned.

**Table and index lifecycle:**

- Create the table at collection-create time, with `embedding vector` untyped.
- On the first write, inside a transaction with `SELECT … FOR UPDATE` on the collection row:
  - set `dimension`;
  - `ALTER COLUMN embedding TYPE vector(d)` (instant on an empty table);
  - create the HNSW index.
- Concurrent first writers serialize on the row lock.

**High dimensions:**

- `vector` HNSW supports at most 2,000 dims.
- For 2,001–4,000 dims, index the expression `(embedding::halfvec(d))` and re-rank the top candidates with full-precision distance.
- Above 4,000 dims, fall back to exact scan.

### 5.3 Configuration and schema

- Store the client-sent `configuration` and `schema` JSON after normalizing it with Chroma's defaults. Echo it back byte-for-byte equivalent.
- Map the fields to Postgres as follows:
  - `space` → opclass
  - `max_neighbors` → `m`
  - `ef_construction` → `ef_construction`
  - `ef_search` → `SET LOCAL hnsw.ef_search` per query
- `num_threads`, `batch_size`, `sync_threshold`, and `resize_factor` are accepted and echoed but are no-ops.
- `spann` config (Cloud) is accepted and echoed, and HNSW is used underneath.
- Updates follow Chroma's rules: only `UpdateHnswConfiguration` fields are mutable, and `space` is immutable.
- On each write, merge the observed `(metadata key, value type)` pairs into `schema.keys`, the same way Chroma does.
- The per-key inverted index settings in `schema` can later drive real Postgres expression indexes (for example btree on `((metadata->>'year')::numeric)`).

### 5.4 Metadata semantics in SQL

- JSONB `numeric` keeps `3.0` vs `3` and compares them as equal, which is exactly Chroma's int and float behavior.
- Each operator compiles to type-guarded SQL:

| Chroma | SQL (`m` = `metadata`) |
|---|---|
| `{"k":{"$eq":v}}` (scalar) | `m @> '{"k":v}'` (GIN) plus a type guard for bool vs number |
| `$ne` | `NOT (m @> '{"k":v}')` (missing key matches) |
| `$gt` etc. | `jsonb_typeof(m->'k')='number' AND (m->>'k')::numeric > $1` |
| `$in` / `$nin` | OR of containments / its negation (missing key matches `$nin`) |
| array `$contains` | `m->'k' @> '[v]'` |
| `#document $contains` | `document LIKE '%' \|\| esc($1) \|\| '%'` (trigram GIN, case-sensitive) |
| `$regex` | `document ~ $1` after dialect translation (below) |

- **Regex dialect.** Chroma uses Rust `regex` syntax. Go `regexp/syntax` is the same RE2 family, so:
  1. Parse the pattern with Go.
  2. Reject what Chroma rejects.
  3. Emit a Postgres ARE-safe pattern.
  4. If translation isn't provably equivalent, post-filter in Go.

- **Sparse vectors** (`SparseVector` metadata values, for Tier B):
  - They are stored in `metadata` for round-trip.
  - They are also indexed in a per-collection **postings table** `(key, dim bigint, rowid, value real)` with a btree on `(key, dim)`.
  - pgvector `sparsevec` is not used, for two reasons:
    - its dimension limit is 1e9, and Chroma's BM25 uses `abs(murmur3)` indices up to about 2.1e9;
    - its HNSW index allows at most 1,000 non-zeros.
  - The postings table also gives document frequencies for BM25 IDF, which the server applies when `bm25: true`: `idf = ln((N − n_t + 0.5)/(n_t + 0.5) + 1)`.

## 6. Query execution

### `get` / `count` / `delete`

```sql
SELECT … WHERE <compiled filter> [AND id = ANY($ids)] ORDER BY rowid LIMIT/OFFSET
```

### `query` (KNN)

```sql
BEGIN;
SET LOCAL hnsw.ef_search = <cfg>;
SET LOCAL hnsw.iterative_scan = strict_order;
SET LOCAL hnsw.max_scan_tuples = <cfg>;
SELECT id, …, embedding <op> $q AS d FROM c_x WHERE <filter> ORDER BY d LIMIT k;
```

- Distances are converted to Chroma's definitions (see §3).
- **Chroma guarantees exactly `min(k, matching)` results under filters, but pgvector's HNSW post-filters.** Iterative scans mostly close this gap. As a backstop, if fewer than `k` rows come back, count the matches, and when `matched > returned`, rerun as an exact scan.
- A query restricted by `ids` always uses an exact scan.
- Multiple query embeddings run as parallel queries on the pool, or as one `LATERAL` join.

### Writes

- Validate, then decode base64, then stage into a temp table with `COPY` (pgx `CopyFrom`), then one statement:

| Operation | Statement |
|---|---|
| `add` | `INSERT … ON CONFLICT (id) DO NOTHING` |
| `upsert` | `ON CONFLICT DO UPDATE` with the JSONB merge `(m || new) - null_keys` |
| `update` | `UPDATE … FROM staged` |

- Duplicate ids within a batch are resolved in Go first, applied sequentially the way Chroma's log would (Postgres errors on `DO UPDATE` touching a row twice).
- Each write bumps `collections.log_position` and stamps `seq`.

### `search` (Tier B)

1. Compile the rank tree to SQL:
   - Each `$knn` leaf becomes a CTE: top-`limit` (default 16) by dense or sparse distance, with `row_number()` if `return_rank`.
   - The CTEs are `FULL OUTER JOIN`ed on `rowid`, and missing entries get `default` (or the record is dropped when `default` is null).
   - Arithmetic maps directly: `$sum`/`$sub`/`$mul`/`$div`/`$abs`/`$exp`/`$log`/`$min`/`$max` → `+ - * / abs exp ln LEAST GREATEST`.
2. RRF is client-side sugar: `-(Σ w/(k+rank))` built from these primitives. Nothing extra is needed.
3. Order ascending (lower is better). Then apply `filter`, `group_by` (`row_number() OVER (PARTITION BY m->'g' ORDER BY <MinK/MaxK keys>)`, keeping ≤ k per group), `limit`/`offset`, and `select` keys (`#id`, `#document`, `#embedding`, `#metadata`, `#score`, metadata fields).
4. A batch `searches[]` executes each entry. `read_level` is accepted and ignored (PG is read-your-writes).

## 7. Cross-cutting

- **Auth**, pluggable:
  - `none` (default, matching OSS);
  - `token` (static tokens from file/env → `{tenant, databases, role}`, read from `Authorization: Bearer` or `X-Chroma-Token`).
- `/auth/identity` reflects the token's tenant and databases. Clients use it to auto-select tenant/db.
- Errors are 401 `AuthError` / 403 `AuthorizationError`.
- **Limits:** body size (default 40 MB, same as Chroma), `max_batch_size` (configurable, default 5461 for parity), per-request timeout, statement timeout.
- **CORS:** allow-origins list, `*` supported.
- **Tracing:** `chroma-trace-id` response header, OTel spans per handler and per SQL statement.
- **Ops:**
  - `kaleid migrate` (catalog);
  - `kaleid serve`;
  - `/healthcheck` checks the DB.
- **Transaction pooling (PgBouncer):** safe, because all `SET LOCAL` runs inside explicit transactions and no session state is used.
- **Maintenance:** per-table autovacuum tuning (HNSW indexes bloat under update/delete churn), and optional `REINDEX CONCURRENTLY` via an admin command.

## 8. Testing strategy

Compatibility is the product, so testing is the backbone.

1. **Differential harness** (`test/compat`, Go):
   - Replays a corpus of HTTP scenarios against **reference Chroma** (`chromadb/chroma:1.5.9` in Docker) and Kaleid.
   - Diffs normalized JSON: UUIDs and timestamps masked, floats compared within ε, map order ignored, status and `error` name compared exactly.
   - Every item in §3 becomes a case.
2. **Upstream conformance.** Run Chroma's own Python suite (`chromadb/test`: 75 files, including the hypothesis property tests with an in-memory reference model) against Kaleid via `CHROMA_SERVER_HOST=host:port`, with a curated skip list (local persistence, restart, fork-local). Also run the JS client suite (`clients/new-js`) where it can target an external server.
3. **Unit tests:** filter compiler (SQL golden tests plus a Go-evaluator vs Postgres property test), rank compiler, wire codecs, validators.
4. **Integration:** testcontainers PG17 + pgvector, covering concurrency (racing first writes, upsert storms), large batches, and dimension edge cases.
5. **Performance:** recall@10 and QPS vs Chroma on 100k/1M × 384/768/1536-dim sets, with and without selective filters.
6. **Search API oracle:** local Chroma has none (`"Search operation is not implemented for local executor"`). Use spec-derived tests (no Chroma Cloud oracle is planned).

## 9. Milestones

| # | Milestone | Contents | Exit criteria |
|---|---|---|---|
| M0 | Skeleton | go.mod, config, chi, error/trace middleware, system endpoints, catalog migrations, docker-compose (pg17 + pgvector), CI, compat harness scaffold | heartbeat/version/pre-flight diff-clean |
| M1 | Control plane | Tenants, databases, collections CRUD; configuration/schema normalization; name validation; list/count pagination | Collection scenarios diff-clean |
| M2 | Write path | Per-collection tables, lazy dimension + index, add/upsert/update/delete, base64, merge semantics, schema key growth, COPY staging | Write scenarios diff-clean |
| M3 | Read path | get/count, full where/where_document compiler, regex translation, query KNN with iterative scan and exact fallback, include handling, distance mapping | Read/query scenarios diff-clean |
| M4 | Conformance | Upstream Python property suite plus JS suite green (minus skip list); fix drift | **v0.1: OSS parity** |
| M5 | Production | Auth modes plus identity, CORS, limits, OTel, metrics, `/openapi.json`, Docker image + goreleaser binaries, halfvec >2000 dims, perf tuning | Recall/QPS report |
| M6 | Cloud parity | Sparse postings plus BM25 IDF, `/search` (rank tree, RRF, group_by, select), CRN lookup, fork/fork_count, indexing_status | **v0.2** |
| M7 | Post-1.5.9 | Conditional transactions (`seq`-based conflict check → 409 `"conditional write conflict"`), databases/by-id | Tracks the next Chroma release |

## 10. Risks

| Risk | Mitigation |
|---|---|
| Filtered ANN returns fewer than `k` rows (pgvector post-filters) | Iterative scan (`strict_order`) plus exact-scan fallback; tests assert counts |
| HNSW insert cost on large bulk loads | COPY staging; batch-size tuning |
| Catalog bloat with a huge number of collections | Per-table design is fine to ~10⁴–10⁵; target is ≤10⁵ collections |
| Regex dialect drift (Rust regex vs PG ARE) | Parse with Go RE2, translate conservatively, post-filter fallback |
| Float formatting (`3.0` vs `3`, f32 repr) round-trip differences | Custom marshalers plus golden tests |
| Chroma changes its API (fast-moving project) | Version-pinned harness; re-run the harness against each new Chroma release |
| Clients depend on message text | Verbatim messages, covered by the differential harness |

## 11. Explicitly not doing

- Embedded clients.
- Server-side embedding. Clients embed, same as Chroma.
- Attached functions.
- Sync service.
- CMEK.
- Copy-on-write forking. Forks copy the data.

## 12. Decisions (2026-09-26)

| Topic | Decision |
|---|---|
| Language | Go |
| Compatibility target | Pin chromadb **1.5.9**. Leave room for master-only features (conditional transactions, databases/by-id) and ship them once Chroma releases them (M7). |
| Scope | OSS parity first (M0–M4), then the Cloud Search API, forking, CRN lookup and indexing_status (M6). |
| Workload | Up to about 100k collections, so one table per collection. No shared-table mode. |
| Postgres | Self-hosted **PG 17**, pgvector ≥0.8, pg_trgm. Kaleid owns its schema and creates tables at runtime. |
| Auth | Pluggable: `none` (default, like OSS) and static `token` mapped to tenant + databases. `basic` and OIDC are dropped. |
| >2,000 dims | halfvec HNSW index for 2,001–4,000 dims with full-precision re-rank; exact scan above 4,000. |
| Filtered query recall | Exact by default: iterative scan plus exact-scan fallback, so `n_results` is always met when enough records match. |
| Migration from Chroma | Not needed. No `import` command. |
| Delivery | Multi-arch Docker image (plus docker-compose with PG 17 + pgvector) and static binaries via goreleaser. No Helm chart. |
| License | MIT |
