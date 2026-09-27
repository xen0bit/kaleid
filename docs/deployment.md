# Deployment

Kaleid is a single stateless binary, and PostgreSQL holds all the data. You
therefore deploy and scale Kaleid like any web service, and protect and back
up the data like any Postgres database.

## PostgreSQL

| Requirement | Why |
|---|---|
| PostgreSQL 17 | The target version; CI runs against it |
| pgvector **0.8.0 or newer** | Iterative index scans, which keep filtered queries accurate |
| `pg_trgm` extension (ships with PostgreSQL) | Index for document `$contains` and `$regex` filters |
| A role that can create schemas and tables | Kaleid creates a table per collection while running |
| For the first start: permission to create extensions, or `vector` and `pg_trgm` already installed | Migrations run `CREATE EXTENSION IF NOT EXISTS` |

Kaleid uses two schemas:

- `kaleid` holds the catalog: tenants, databases and collections.
- `kaleid_data` holds one table per collection.

It can share a database with other applications, but a dedicated database is
simpler to operate.

**Shared memory.** Building an HNSW index in parallel needs dynamic shared
memory. Docker gives containers only 64 MB of `/dev/shm` by default, so give
PostgreSQL containers more:

- `shm_size: 1gb` in Compose;
- `--shm-size=1g` with `docker run`.

If a parallel build still runs out, Kaleid retries it as a slower serial
build.

**Managed PostgreSQL** (RDS, Cloud SQL, Azure, Supabase, Neon and others)
works if the service offers pgvector 0.8 or newer and lets your role create
the extensions. Check the provider's pgvector version first.

**Connection pooling.** Kaleid pools its own connections. It also works
behind PgBouncer in transaction mode, because it keeps no session state
between transactions.

## Docker

```sh
docker build -t kaleid .
docker run -d -p 8000:8000 \
  -e KALEID_DATABASE_URL='postgres://kaleid:secret@db:5432/kaleid?sslmode=disable' \
  kaleid
```

The image is based on distroless and runs as a non-root user. Tagged releases
are published to `ghcr.io/xen0bit/kaleid` for amd64 and arm64.

[`docker-compose.yml`](../docker-compose.yml) runs PostgreSQL 17 with pgvector
alongside Kaleid, which is handy for development.

## Binary and systemd

Release archives contain static binaries for Linux, macOS and Windows on amd64
and arm64. To build one yourself:

```sh
CGO_ENABLED=0 go build -trimpath -o kaleid ./cmd/kaleid
```

[`examples/deployments/systemd`](../examples/deployments/systemd) has a
hardened unit file and an environment file.

## Scaling and availability

- **Replicas.** Run as many Kaleid replicas as you like behind a load
  balancer. They coordinate only through PostgreSQL. Writes to the same
  collection are serialized by a PostgreSQL advisory lock; writes to
  different collections proceed in parallel.
- **Health checks.** `GET /api/v2/healthcheck` returns 200 when PostgreSQL is
  reachable and 503 when it isn't. `GET /api/v2/heartbeat` shows only that
  the process is up.
- **Availability of the data** comes from PostgreSQL: use streaming
  replication with failover. Kaleid reconnects automatically.
- **Read scaling.** Kaleid sends all queries to the `KALEID_DATABASE_URL`
  primary. Run replicas against replica databases only if they serve reads
  alone, and be aware that replication lag makes those reads slightly stale.

## TLS

Kaleid serves plain HTTP. Terminate TLS in front of it, with a load balancer,
ingress or reverse proxy, or keep it on a private network. Connections to
PostgreSQL are encrypted according to `sslmode` in the database URL.
