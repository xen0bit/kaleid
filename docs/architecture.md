# Architecture

```
clients ──HTTP──▶ kaleid (stateless Go, N replicas)
                   ├─ api        routes, auth, request validation, Chroma-shaped errors
                   ├─ collection configuration and schema reconciliation (a port of Chroma's rules)
                   ├─ filter     where / where_document → SQL; Rust regex → PostgreSQL regex
                   ├─ search     rank expressions, RRF, group_by
                   └─ store      pgx connection pool, table-per-collection storage, index lifecycle
                                   │
                                   ▼
                        PostgreSQL 17 + pgvector + pg_trgm
```

| Package | Role |
|---|---|
| `cmd/kaleid` | Entry point |
| `internal/api` | HTTP handlers and error responses |
| `internal/collection` | Configuration and schema model |
| `internal/filter` | Filter parsing and SQL compilation |
| `internal/search` | Search API model and ranking |
| `internal/store` | PostgreSQL storage |
| `internal/wire` | JSON codecs that keep ints and floats distinct |
| `pkg/client`, `pkg/embed` | Public Go client and embedders |

## Storage

The **catalog** lives in the `kaleid` schema:

- `tenants`
- `databases`
- `collections`, which holds each collection's metadata, schema (JSONB),
  dimension, index state and write counter

A collection's `schema` is the source of truth. `configuration_json` is
derived from it on every read, exactly as Chroma does.

The **records** of each collection live in their own table:

```sql
CREATE TABLE kaleid_data."c_<id>" (
  rowid     bigserial PRIMARY KEY,  -- insertion order
  id        text NOT NULL UNIQUE,   -- the Chroma record id
  embedding vector(<d>),            -- dimension set by the first write
  document  text,
  uri       text,
  metadata  jsonb NOT NULL DEFAULT '{}',
  seq       bigint NOT NULL
);
-- GIN (metadata jsonb_path_ops), GIN (document gin_trgm_ops), and the ANN index
```

Each collection gets its own table because every collection can have a
different dimension and distance. pgvector indexes a column of one dimension
with one operator class, so a table per collection gives each collection:

- a typed column;
- an index with the right operator class;
- a `DROP TABLE` to delete it;
- queries that only ever scan its own rows.

Next to it, a postings table, `kaleid_data."s_<id>"(key, dim, rid, value)`,
stores every sparse vector found in metadata. Sparse search reads it for
dot products and BM25 document frequencies. pgvector's own `sparsevec` type
isn't used, for two reasons: it allows indexes only up to dimension 10⁹
(Chroma's BM25 term hashes go past that), and its HNSW index handles at most
1,000 non-zero values per vector.

## Writes

Every write to a collection runs in one transaction:

1. **Lock the collection.** Take its advisory lock and re-read its catalog
   row.
2. **Set the dimension on first write.** Change the column to `vector(d)`,
   which is instant on an empty table, and mark the index `pending`.
3. **Load existing state.** Read the batch's existing rows (metadata,
   document, URI).
4. **Apply the operations in order**, with Chroma's log semantics:
   - `add` skips ids that already exist;
   - `update` skips ids that don't;
   - `upsert` and `update` merge metadata, and a `null` value deletes a key;
   - duplicate ids within a batch resolve as if applied one after another.
5. **Write in bulk** with `INSERT … SELECT FROM unnest(...)` and
   `UPDATE … FROM unnest(...)`, sending vectors in pgvector's binary format.
6. **Maintain side tables.** Refresh the sparse postings, add schema entries
   for new metadata keys, and build the HNSW index if the collection just
   reached `--index-threshold`.

## Filters

`where` clauses compile to parameterized SQL over the JSONB `metadata` column:

| Chroma | SQL |
|---|---|
| `{"k": v}` / `$eq` | `metadata @> '{"k": v}'` (uses the GIN index; JSON numbers compare by value) |
| `$ne` | `NOT (metadata @> …)`, which is true when the key is missing |
| `$gt` / `$gte` / `$lt` / `$lte` | `CASE WHEN jsonb_typeof(metadata->'k') = 'number' THEN (metadata->>'k')::numeric > $1 END` |
| `$in` / `$nin` | `(metadata->'k') = ANY($1::jsonb[])` / its negation |
| array `$contains` | `metadata @> '{"k": [v]}'` |
| document `$contains` | `document LIKE '%…%'` with escaping (trigram-indexed, case-sensitive) |
| document `$regex` | `document ~ $1` |

Floats are stored with a decimal point (`3.0`). PostgreSQL's JSONB `numeric`
keeps that, which is how Kaleid tells ints from floats.

Regex patterns are parsed with Go's `regexp/syntax`, which comes from the same
RE2 family as Rust's `regex` crate. The syntax tree is then written back out
as a PostgreSQL regular expression. This avoids dialect traps; for example,
`\b` means "word boundary" in RE2 but "backspace" in PostgreSQL.

## Queries

A `query` builds this SQL for each query vector:

```sql
SET LOCAL hnsw.ef_search = max(ef_search, n_results);
SET LOCAL hnsw.iterative_scan = strict_order;
SELECT …, <distance> FROM c_<id> WHERE <filter> ORDER BY embedding <op> $q LIMIT n;
```

Distances are converted to Chroma's definitions: squared L2, `1 - cos`, and
`1 - dot`. The query runs as an exact scan instead when:

- the collection has no built index;
- the query restricts to explicit `ids`;
- the approximate results came back short while more records match the
  filter.

## Search

A search runs in these steps:

1. **KNN leaves.** Each KNN leaf of the rank expression runs as its own
   query: dense leaves use the path above, and sparse leaves use a join over
   the postings table. Both apply the filter.
2. **Combine the scores.** Kaleid evaluates the expression the same way
   Chroma's worker does. Each subexpression is a map from record to score,
   plus an optional default, combined arithmetically (see
   [Search API](search.md#rank-expressions)).
3. **Group, page and project.** Apply `group_by`, then pagination, then load
   the selected fields.
