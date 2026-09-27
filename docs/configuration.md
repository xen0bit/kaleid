# Configuration

Kaleid is configured with flags or the matching environment variables. When
both are given, the flag wins.

```sh
kaleid serve --database-url postgres://... --listen :8000
KALEID_DATABASE_URL=postgres://... kaleid          # "serve" is the default command
kaleid migrate                                      # apply schema migrations, then exit
kaleid version
```

## Options

| Flag | Environment | Default | Description |
|---|---|---|---|
| `--database-url` | `KALEID_DATABASE_URL` | (required) | PostgreSQL connection URL, e.g. `postgres://user:pass@host:5432/db?sslmode=require` |
| `--listen` | `KALEID_LISTEN` | `:8000` | HTTP listen address |
| `--auth` | `KALEID_AUTH_PROVIDER` | `none` | `none` or `token`; see [Authentication](#authentication) |
| `--auth-token` | `KALEID_AUTH_TOKEN` | | A single token with full access |
| `--auth-tokens-file` | `KALEID_AUTH_TOKENS_FILE` | | JSON file of tokens, each scoped to a tenant and databases |
| `--allow-reset` | `KALEID_ALLOW_RESET` | `false` | Enables `POST /api/v2/reset`, which **deletes all data**. For tests only. |
| `--cors-allow-origins` | `KALEID_CORS_ALLOW_ORIGINS` | | Comma-separated origins allowed to call from browsers, or `*` |
| `--max-batch-size` | `KALEID_MAX_BATCH_SIZE` | `5461` | Batch size advertised to clients; they split larger writes into batches this size |
| `--max-payload-bytes` | `KALEID_MAX_PAYLOAD_BYTES` | `41943040` | Largest request body accepted (40 MiB) |
| `--index-threshold` | `KALEID_INDEX_THRESHOLD` | `10000` | Number of records at which a collection's HNSW index is built; `0` builds it immediately. See [Operations](operations.md#vector-indexes). |
| `--index-build-memory` | `KALEID_INDEX_BUILD_MEMORY` | `256MB` | `maintenance_work_mem` for index builds |
| `--exact-fallback` | `KALEID_EXACT_FALLBACK` | `true` | Reruns a filtered query as an exact scan when the index returns fewer results than exist |
| `--max-scan-tuples` | `KALEID_MAX_SCAN_TUPLES` | `20000` | `hnsw.max_scan_tuples`: how far an iterative index scan goes before giving up |
| `--log-level` | `KALEID_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON on stderr |

## Authentication

Open-source Chroma doesn't authenticate requests, so by default neither does
Kaleid. Either put it behind a proxy that authenticates, or turn on token
auth:

```sh
KALEID_AUTH_PROVIDER=token KALEID_AUTH_TOKEN=s3cret kaleid
```

With token auth on:

- Clients send the token as `Authorization: Bearer <token>` or
  `X-Chroma-Token: <token>`.
- Requests with a missing or unknown token get `401`.
- Requests outside the token's scope get `403`.
- The heartbeat, version, healthcheck and pre-flight endpoints stay public,
  as in Chroma.

To give different callers different access, list the tokens in a file:

```json
[
  {"token": "admin-secret"},
  {"token": "team-a-secret", "user_id": "team-a", "tenant": "default_tenant", "databases": ["team_a"]},
  {"token": "reader-secret", "user_id": "analytics", "tenant": "acme", "databases": ["*"]}
]
```

```sh
KALEID_AUTH_PROVIDER=token KALEID_AUTH_TOKENS_FILE=/etc/kaleid/tokens.json kaleid
```

Scoping rules:

- An entry with no `tenant` can access every tenant. `"*"` means the same.
- An entry with no `databases` can access every database in its tenant.
  `["*"]` means the same.
- `GET /api/v2/auth/identity` returns the caller's `user_id`, tenant and
  databases. Chroma's clients use it to choose a default tenant and database.
- `POST /api/v2/reset` also requires a token with access to every tenant.

Kaleid stores only SHA-256 hashes of tokens in memory and compares them in
constant time. Use long random tokens, for example from
`openssl rand -hex 32`, and serve Kaleid over TLS, either with a TLS proxy
in front or on a private network.
