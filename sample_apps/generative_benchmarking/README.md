# Generative benchmarking

Public embedding benchmarks rarely look like your data. This tool builds a
retrieval benchmark from your own documents and uses it to compare embedding
models. It ports Chroma's
[generative benchmarking toolkit](https://github.com/chroma-core/chroma/tree/main/sample_apps/generative_benchmarking),
which accompanies Chroma's
[technical report](https://research.trychroma.com/generative-benchmarking),
from Python notebooks to a Go command line. The prompts, criteria and
metrics are the same.

## Pipeline

| Step | Command | What happens |
|---|---|---|
| 1 | `filter` | An LLM keeps documents that are relevant to your use case and complete enough to answer questions. Output: `data/filtered_ids.json`. |
| 2 | `generate` | An LLM writes one realistic user query per kept document, in the style of your example queries. Output: `data/queries.json`. |
| 3 | `evaluate` | Loads the corpus into Kaleid with the embedder chosen by `EMBEDDER`. Each query retrieves its top 10, and the query's own document counts as the relevant one. Output: `results/<timestamp>.json`. |
| 4 | `compare` | Prints results files side by side. |

The metrics are NDCG, MAP, Recall and Precision at 1, 3, 5 and 10, computed
exactly as `pytrec_eval` does in the original.

## Running

The default corpus, `data/chroma_docs.json`, is Chroma's documentation split
into chunks, as in the original toolkit (Apache-2.0, see [NOTICE](../../NOTICE)).

```sh
cd sample_apps/generative_benchmarking

OPENAI_API_KEY=sk-... go run . filter
OPENAI_API_KEY=sk-... go run . generate

# Evaluate one or more embedders; each run writes a results file.
EMBEDDER=openai OPENAI_API_KEY=sk-... EMBEDDING_MODEL=text-embedding-3-small go run . evaluate
EMBEDDER=openai OPENAI_API_KEY=sk-... EMBEDDING_MODEL=text-embedding-3-large go run . evaluate
EMBEDDER=ollama EMBEDDING_MODEL=nomic-embed-text go run . evaluate

go run . compare results/*.json
```

To use your own data:

- `-corpus` takes a JSON file of the form `{"id": "text", ...}`.
- `-context` describes your use case, for `filter` and `generate`.
- `-examples` gives a few typical user queries, one per line, for `generate`.

`generate -heuristic` makes keyword queries without an LLM. It only checks
that the pipeline works end to end; its scores do not measure an embedder.
