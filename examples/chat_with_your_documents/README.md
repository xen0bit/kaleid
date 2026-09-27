# Chat with your documents

This is a minimal retrieval-augmented chat app. It ports Chroma's
[`chat_with_your_documents`](https://github.com/chroma-core/chroma/tree/main/examples/chat_with_your_documents)
example and its [`gemini`](https://github.com/chroma-core/chroma/tree/main/examples/gemini)
and [`xai`](https://github.com/chroma-core/chroma/tree/main/examples/xai)
variants. The variants differ only in which LLM answers, so here that's the
`LLM_PROVIDER` setting.

The sample documents in `documents/` are the 2022 and 2023 U.S. State of the
Union addresses, which are in the public domain.

## How it works

1. `load` reads the files in `documents/` line by line. It embeds each
   non-empty line and stores it with its file name and line number.
2. `chat` embeds each question you ask and retrieves the five most relevant
   lines.
3. The question and those lines go to an LLM, which is told to answer only
   from that context.
4. The app prints the answer, followed by the source lines it used.

## Running

From the repo root, with Kaleid running on `localhost:8000`:

```sh
# Load the documents. Use a real embedding model for meaningful retrieval.
EMBEDDER=openai OPENAI_API_KEY=sk-... go run ./examples/chat_with_your_documents load

# Chat. Use the same embedder you loaded with.
EMBEDDER=openai OPENAI_API_KEY=sk-... go run ./examples/chat_with_your_documents chat
```

Other LLM providers work the same way:

```sh
LLM_PROVIDER=ollama go run ./examples/chat_with_your_documents chat                      # local: ollama pull llama3.2
LLM_PROVIDER=gemini GEMINI_API_KEY=... go run ./examples/chat_with_your_documents chat
LLM_PROVIDER=xai XAI_API_KEY=... go run ./examples/chat_with_your_documents chat
LLM_MODEL=gpt-4.1 go run ./examples/chat_with_your_documents chat                        # override the model
```

To see only the retrieval step, without calling an LLM:

```sh
go run ./examples/chat_with_your_documents chat -no-llm -question "What did the president say about inflation?"
```

Flags:

- `load -dir DIR` loads your own `.txt` files instead of the samples.
- `load -reset` starts from an empty collection.
- `chat -n N` sets how many lines to retrieve.
