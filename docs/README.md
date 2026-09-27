# Kaleid documentation

Kaleid is a vector database server that speaks Chroma's v2 HTTP API and
stores everything in PostgreSQL with pgvector. Existing Chroma clients work
against it unchanged, and your data lives in ordinary Postgres tables.

| Page | For |
|---|---|
| [Getting started](getting-started.md) | Running Kaleid and storing and querying your first vectors |
| [Clients](clients.md) | Connecting from Python, JavaScript, Rust and Go, plus embeddings |
| [Search API](search.md) | Hybrid search, RRF, BM25, `group_by` and projection |
| [Configuration](configuration.md) | Every flag and environment variable, and authentication |
| [Deployment](deployment.md) | Docker, binaries, systemd, PostgreSQL requirements, scaling |
| [Operations](operations.md) | Health checks, backups, index tuning, maintenance, upgrades |
| [Compatibility](compatibility.md) | What matches Chroma 1.5.9, what differs, and how that's tested |
| [Architecture](architecture.md) | How collections, indexes, filters and search map onto PostgreSQL |

There are also runnable [examples](../examples) and [sample apps](../sample_apps).
