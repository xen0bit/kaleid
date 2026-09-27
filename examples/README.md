# Examples

These are Go ports of Chroma's
[examples](https://github.com/chroma-core/chroma/tree/main/examples), built on
Kaleid's Go client (`pkg/client`). Each one is a small, self-contained
program. They also run against Chroma itself, except `forking` and
`hybrid_search`, which need features that only Chroma Cloud has.

| Example | Chroma original | Shows |
|---|---|---|
| [`start_here`](start_here) | `basic_functionality/start_here.ipynb` | Store passages and retrieve the best one for each question (SciQ) |
| [`where_filtering`](where_filtering) | `where_filtering.ipynb`, `in_not_in_filtering.ipynb` | Metadata and document filters: `$and`, `$or`, `$in`, `$nin`, `$contains` |
| [`embeddings`](embeddings) | `alternative_embeddings.ipynb` | The same workflow with different embedding providers |
| [`auth`](auth) | `auth.ipynb` | Token auth, with a bearer token or `X-Chroma-Token` |
| [`forking`](forking) | `advanced/forking.ipynb` | Branch a collection, change the branch, keep the original |
| [`hybrid_search`](hybrid_search) | Chroma's hybrid search docs | Dense and BM25 retrieval fused with RRF, plus `group_by` |
| [`chat_with_your_documents`](chat_with_your_documents) | `chat_with_your_documents`, `gemini`, `xai` | Retrieval-augmented chat over local files |
| [`deployments/systemd`](deployments/systemd) | `deployments/systemd-service` | Running Kaleid as a systemd service |

The larger apps are in [`../sample_apps`](../sample_apps).

## Running

Start Kaleid (for example `docker compose up -d` from the repo root), then
run an example from the repo root:

```sh
go run ./examples/start_here
```

| Variable | Default | Meaning |
|---|---|---|
| `KALEID_URL` | `http://localhost:8000` | Server to talk to. Chroma works too. |
| `KALEID_TOKEN` | | Bearer token, if the server requires one |
| `EMBEDDER` | `hash` | `hash`, `openai`, `ollama` or `openai-compatible` (see [`pkg/embed`](../pkg/embed)) |
| `LLM_PROVIDER` | `openai` | For examples that chat: `openai`, `ollama`, `gemini`, `xai` or `openai-compatible` |

### About embeddings

Chroma's Python and JS clients embed text for you. In Go you embed it
yourself and send the vectors; `pkg/embed` provides the embedders.

By default the examples use `EMBEDDER=hash`. It is deterministic and needs no
model, no API key and no network, so every example runs anywhere, including
in CI. It matches on shared words, not meaning. To see real semantic search,
use a model:

```sh
EMBEDDER=openai OPENAI_API_KEY=sk-... go run ./examples/start_here -sciq 2000
EMBEDDER=ollama go run ./examples/start_here        # needs `ollama pull nomic-embed-text`
```

A collection must be read with the same embedder that wrote it. If you switch
embedders, reload the data.

## What was not ported

- **`local_persistence`**: Kaleid always stores data in PostgreSQL, so there
  is nothing extra to set up.
- **`multimodal`, `server_side_embeddings`**: these need image models or
  server-side embedding, which Kaleid does not do. You can still store image
  embeddings from any model you run yourself.
- **`conditional_transactions.py`, `task_api_example.py`**: these depend on
  Chroma features that Kaleid does not implement: conditional transactions
  (not yet in a Chroma release) and attached functions.
- **`observability`**: Kaleid logs structured JSON and serves
  `/api/v2/healthcheck`; see [docs/operations.md](../docs/operations.md).
- **`use_with/*`** (Cohere, Jina, Roboflow, Ollama): any provider with an
  OpenAI-compatible embeddings API works through `embed.OpenAICompatible`.
  Ollama is built in.
