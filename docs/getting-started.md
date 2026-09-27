# Getting started

## 1. Run Kaleid

The quickest way is Docker Compose. From the repository root:

```sh
docker compose up -d
curl localhost:8000/api/v2/heartbeat
# {"nanosecond heartbeat":1790481234567890123}
```

This starts PostgreSQL 17 with pgvector, and Kaleid on port 8000. Kaleid
creates its schema on first start.

To use a PostgreSQL you already run, build the binary and point it at the
database. The requirements are listed in [Deployment](deployment.md#postgresql).

```sh
go build -o kaleid ./cmd/kaleid
KALEID_DATABASE_URL=postgres://user:pass@db.example.com:5432/kaleid ./kaleid
```

## 2. Connect a client

Kaleid speaks Chroma's API, so any Chroma client works. In Python:

```sh
pip install chromadb-client
```

```python
import chromadb

client = chromadb.HttpClient(host="localhost", port=8000)
collection = client.get_or_create_collection("docs")

# The Python client embeds documents with Chroma's default model.
collection.add(
    ids=["a", "b", "c"],
    documents=[
        "PostgreSQL supports point-in-time recovery",
        "Go has lightweight goroutines",
        "pgvector adds HNSW indexes to Postgres",
    ],
    metadatas=[{"topic": "db"}, {"topic": "lang"}, {"topic": "db"}],
)

results = collection.query(
    query_texts=["vector indexes in a database"],
    n_results=2,
    where={"topic": "db"},
)
print(results["ids"])  # [['c', 'a']]
```

In Go, with the client in this repository:

```go
import (
	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

c := client.New("http://localhost:8000")
col, _ := c.CreateCollection(ctx, "docs", &client.CreateCollectionOptions{GetOrCreate: true})

emb := embed.NewOpenAI(os.Getenv("OPENAI_API_KEY"), "text-embedding-3-small")
docs := []string{"PostgreSQL supports point-in-time recovery", "pgvector adds HNSW indexes to Postgres"}
vecs, _ := emb.Embed(ctx, docs)
_ = col.Add(ctx, client.Records{IDs: []string{"a", "c"}, Embeddings: vecs, Documents: docs})

q, _ := emb.Embed(ctx, []string{"vector indexes in a database"})
res, _ := col.Query(ctx, client.QueryOptions{Embeddings: q, NResults: 1})
fmt.Println(res.IDs[0]) // the most relevant id first
```

The Go client doesn't embed text itself. You compute the vectors with a
model and pass them in; [Clients](clients.md#embeddings) explains the options.

## 3. Next steps

- **Filter results.** Filter by metadata and document text; see the
  [`where_filtering` example](../examples/where_filtering).
- **Combine semantic and keyword search.** Use the [Search API](search.md).
- **Require credentials.** Turn on token auth; see
  [Configuration](configuration.md#authentication).
- **Go to production.** Read [Deployment](deployment.md) and
  [Operations](operations.md).
