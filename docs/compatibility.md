# Compatibility

Kaleid targets **Chroma 1.5.9**. For each request, the goal is the same status
code, JSON shape, error class and, nearly always, the same error message.

## How it is tested

Every pull request runs these checks in CI:

1. **Chroma's own test suite at the 1.5.9 tag**, pointed at Kaleid through
   Chroma's integration-test mode. This covers the API tests and the
   property-based tests for add, collections, tenants and databases,
   embeddings and filtering. The property tests check results against an
   in-memory reference model.
   [`test/upstream/deselect.txt`](../test/upstream/deselect.txt) lists the
   excluded tests, each with a reason. Most start their own in-process Chroma
   and never contact the server under test.
2. **Differential tests** ([`test/compat`](../test/compat)). About 160
   requests are sent to the official `chromadb/chroma:1.5.9` image and to
   Kaleid, and the responses are compared after normalization. The status
   code, error class and message must match, and every number must keep its
   int-or-float type. The Go client's test flow also runs against both
   servers.
3. **Integration tests** ([`test/integration`](../test/integration)) cover
   what local Chroma can't serve: the Search API, sparse vectors and BM25,
   forking, auth, and recall with high-dimension indexes.

## Endpoints

| Endpoint | Status |
|---|---|
| Heartbeat, version, healthcheck, pre-flight checks, auth identity, reset | Supported |
| Tenants: create, get, update | Supported |
| Databases: create, list, get, get by id, delete | Supported |
| Collections: create (with `get_or_create`), list, count, get by name, id or CRN, update, delete | Supported |
| Records: add, upsert, update, delete, get, count, query | Supported |
| Search API | Supported. In Chroma this is Chroma Cloud only. |
| Fork, fork count, indexing status | Supported. In Chroma these are Chroma Cloud only. |
| Attached functions | `501`. Also unsupported in Chroma's open-source server. |
| Conditional transactions | Not yet; they are not in a Chroma release |
| `/api/v1/*` | `410 Gone`, the same as Chroma 1.x |

## Behaviours matched on purpose

These Chroma behaviours are easy to get wrong in a reimplementation:

**Writes**

- Adding an existing id is silently ignored.
- Updating a missing id is silently ignored.
- `update` and `upsert` merge metadata, and a `null` value deletes the key. A
  `null` document in `update` leaves the document unchanged.
- Updating a collection's metadata replaces it entirely.
- The first write sets a collection's dimension. Later mismatches fail with
  `Collection expecting embedding with dimension of N, got M`.

**Reads**

- `get` returns records in insertion order, even when you ask for specific
  ids.
- Tied distances are broken by insertion order.
- A query whose `ids` include one that doesn't exist fails with `Error finding
  id`, as in Chroma.

**Filters**

- A `where` clause must have exactly one top-level key; combine conditions
  with `$and`.
- `$ne` and `$nin` match records that don't have the key. `$eq` and `$in`
  don't.
- Ints and floats compare as numbers (`3` matches `3.0`), but a bool never
  equals a number.
- `$gt`, `$gte`, `$lt` and `$lte` accept only numbers.
- `$contains` on a metadata key tests whether an array contains a value.
  `$contains` on documents is a case-sensitive substring match.
- `$regex` is allowed only on documents, and uses Rust/RE2 syntax.
- Filtering on a key whose index the collection's schema disables is
  rejected, as is document filtering when full-text search is disabled.

**Distances and deletes**

- `l2` distances are squared; `ip` distances are `1 - dot`.
- Deleting by ids reports the number of ids requested.

**Collections**

- `configuration_json` and `schema` include every default.
- Legacy `hnsw:*` metadata keys set the index configuration.
- A requested SPANN configuration is converted to HNSW with the same space.
- The schema gains a key entry for each new metadata key and type.

## Differences

| Area | Chroma 1.5.9 (local) | Kaleid |
|---|---|---|
| Comparing a float with an int | Truncates the float, so `{"$gte": 2.5}` matches `2` | Exact numeric comparison |
| Array metadata after deleting a collection | Left behind; reappears on unrelated records in collections created later | Removed with the collection |
| Forks | Chroma Cloud only; copy-on-write | Available everywhere; a full copy made in one transaction |
| `read_level` | Chooses between reading the index only or the index plus the write-ahead log | Accepted and ignored: every committed write is visible to the next read |
| Regex error messages, and messages Chroma prints from Rust debug output | Rust formatting | Different wording; same status code and error class |
| Renaming a collection to a name that's taken | `500` with a SQLite error | `409 Collection [x] already exists` |
| Sparse vector indexes | Rejected by local Chroma (Chroma Cloud only) | Supported |

## Clients

- **Python** `HttpClient` and `AsyncHttpClient` are tested in CI.
- **JavaScript** and **Rust** clients use the same REST API.
- **Go:** `pkg/client` is tested against both Kaleid and Chroma.
- Chroma's **in-process clients** (`PersistentClient`, `EphemeralClient`)
  can't connect to any server, including Kaleid.
