# Sample apps

These are Go ports of Chroma's
[sample apps](https://github.com/chroma-core/chroma/tree/main/sample_apps).
They're built on Kaleid's Go client (`pkg/client`) and embedders
(`pkg/embed`).

| App | Chroma original | What it does |
|---|---|---|
| [`movies`](movies) | `sample_apps/movies` (Next.js) | Web app: hybrid search and LLM chat over a movies collection |
| [`generative_benchmarking`](generative_benchmarking) | `sample_apps/generative_benchmarking` (notebooks) | Build a retrieval benchmark from your own documents and use it to compare embedding models |

Both read `KALEID_URL`, `KALEID_TOKEN`, `EMBEDDER` and `LLM_PROVIDER`, like
the [examples](../examples#running) do.
