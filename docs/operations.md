# Operations

## Health and logs

| Endpoint | Returns |
|---|---|
| `GET /api/v2/healthcheck` | 200 when PostgreSQL is reachable, 503 when it isn't |
| `GET /api/v2/heartbeat` | The server time in nanoseconds; shows the process is up |
| `GET /api/v2/version` | The API version (`"1.0.0"`, the same as Chroma's server) |
| `GET /openapi.json`, `/docs` | The OpenAPI document and a Swagger UI |

Logs are JSON lines on stderr. Unexpected errors are logged with the method
and path. Every response has a `chroma-trace-id` header, which clients report
in their errors, so you can match a client error to a server log line.

## Backups

All data lives in PostgreSQL, in the `kaleid` and `kaleid_data` schemas, so
you back it up with the usual PostgreSQL tools:

- **Continuous backups with point-in-time recovery:** base backups plus WAL
  archiving, with tools such as pgBackRest, Barman or WAL-G, or your managed
  provider's backups.
- **Logical dumps:** `pg_dump -n kaleid -n kaleid_data`. Restore them into a
  database that already has the `vector` and `pg_trgm` extensions.

Vector indexes are ordinary PostgreSQL indexes, so they are restored along
with the data. Kaleid's binary holds no state.

## Vector indexes

Each collection has one table, `kaleid_data.c_<collection id without dashes>`.
The vector column of that table is indexed as follows:

| Collection | Index | Queries |
|---|---|---|
| Fewer records than `--index-threshold` (default 10,000) | None | Exact scan, so every result is the true nearest neighbour |
| At or above the threshold, up to 2,000 dimensions | HNSW on `vector` | Approximate, with iterative scans for filtered queries |
| At or above the threshold, 2,001–4,000 dimensions | HNSW on `halfvec` | Approximate, then re-ranked at full precision |
| More than 4,000 dimensions | None | Exact scan |

**When the index is built.** The build happens inside the write that crosses
the threshold, which makes that one write slower. After that, each new record
is inserted into the graph as it arrives. That per-record work is what makes
ingest slower once a collection is indexed. For a large bulk load, raise
`--index-threshold` above the final size, load the data, then lower the
threshold again; the index is built on the next write.

**Build parameters** come from the collection's configuration, the same
settings Chroma uses:

| Chroma setting | pgvector parameter | Limits |
|---|---|---|
| `max_neighbors` | `m` | Clamped to 2–100 |
| `ef_construction` | `ef_construction` | At least `2 × m`, at most 1000 |
| `space` | Operator class: `l2`, `cosine` or `ip` | |

If you change `max_neighbors` with a collection update, Kaleid rebuilds the
index. `ef_search` takes effect on the next query; it is raised to at least
`n_results` and capped at 1000.

**Filtered queries.** pgvector applies filters after walking the index.
Kaleid turns on iterative scans (`hnsw.iterative_scan = strict_order`,
bounded by `--max-scan-tuples`). If a filtered query still returns fewer
results than match the filter, it reruns the query as an exact scan, so
queries return the full `n_results` whenever that many records match. To
trade that guarantee for latency, set `--exact-fallback=false`.

**Where to look.** Each collection's catalog row stores its index state in
`kaleid.collections.index_state`: `pending`, `vector`, `halfvec` or `none`.

## Maintenance

- **Autovacuum.** Heavy `update`, `upsert` and `delete` traffic leaves dead
  rows and bloats indexes, especially HNSW indexes. Leave autovacuum on, and
  consider more aggressive settings for tables with high churn.
- **Reindexing.** To rebuild a bloated HNSW index without blocking writes,
  run `REINDEX INDEX CONCURRENTLY kaleid_data."c_<id>_ann"`.
- **Deleted collections.** Deleting a collection drops its tables, so space
  is returned at once and there is nothing to vacuum.

## Upgrades

Kaleid applies catalog migrations in order at startup, under an advisory lock,
so several replicas can start at the same time safely. To run migrations as a
separate deploy step, run `kaleid migrate`. Migrations only move forward, so
back up before upgrading across versions.

## Resetting

`POST /api/v2/reset` deletes every tenant, database and collection. It is off
unless you start Kaleid with `--allow-reset`, and Chroma's test suite is the
only thing that should need it.
