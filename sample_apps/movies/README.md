# Movies

Search and chat with a movies collection. This ports Chroma's
[movies sample app](https://github.com/chroma-core/chroma/tree/main/sample_apps/movies)
from Next.js to a single Go binary with an embedded web page.

- **Search tab.** Hybrid search: a dense-embedding ranking and a BM25 keyword
  ranking over the movie overviews, fused with reciprocal rank fusion. This
  is the same strategy as the original, which uses Chroma Cloud's Search
  API. Kaleid serves that API on any deployment.
- **Chat tab.** An LLM answers questions by calling a `searchMovies` tool
  backed by the same hybrid search. It gets at most five steps, as in the
  original.

## Data

`load` ships with a built-in sample of 60 well-known films, from 1927 to
2016. Titles, years and languages are factual; the plot summaries were
written for this app.

For the original app's scale, download `movies_metadata.csv` from
[The Movies Dataset](https://www.kaggle.com/datasets/rounakbanik/the-movies-dataset)
(about 45,000 films, CC0) and load that instead:

```sh
go run ./sample_apps/movies load -csv movies_metadata.csv
go run ./sample_apps/movies load -csv movies_metadata.csv -limit 5000   # a subset
```

## Running

From the repo root, with Kaleid running on `localhost:8000`:

```sh
# 1. Load the collection. The dense embedder is chosen by EMBEDDER; a real
#    model gives much better semantic matches.
EMBEDDER=openai OPENAI_API_KEY=sk-... go run ./sample_apps/movies load

# 2. Serve the app on http://localhost:3000. Use the same embedder you
#    loaded with. The LLM, which chat needs, is chosen by LLM_PROVIDER.
EMBEDDER=openai OPENAI_API_KEY=sk-... go run ./sample_apps/movies serve
```

- Without an LLM configured, search still works and chat is disabled.
- `LLM_PROVIDER=ollama`, `gemini` and `xai` work too; see the
  [examples README](../../examples#running).
- `serve -addr :8080` changes the port.

## API

The page talks to two JSON endpoints, which you can also call directly:

| Endpoint | Body | Returns |
|---|---|---|
| `GET /api/search?q=...` | | `{"results": [{id, overview, score, metadata}]}`, top 10 by fused rank |
| `POST /api/chat` | `{"messages": [{"role": "user", "content": "..."}]}` | `{"reply", "searches", "results"}` |
