// Package embed turns text into vectors for use with pkg/client.
//
// Chroma's Python and JS clients embed text client-side; this package plays
// that role for Go. It provides:
//
//   - Hashing: an offline, deterministic embedder (no model, no network).
//     Good for tests, demos and CI; not a semantic model.
//   - OpenAICompatible: any server implementing the OpenAI embeddings API
//     (OpenAI, Ollama, vLLM, LM Studio, Azure-compatible gateways, ...).
//   - BM25: a sparse encoder for keyword search, used with a collection
//     whose schema enables a BM25 sparse vector index.
package embed

import (
	"context"
	"fmt"
	"os"
	"strconv"
)

// Embedder turns texts into dense vectors.
type Embedder interface {
	// Embed returns one vector per text.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// Name identifies the embedder (useful for naming collections).
	Name() string
}

// FromEnv builds an embedder from environment variables:
//
//	EMBEDDER=hash (default)      Hashing, dimension EMBEDDING_DIM (default 384)
//	EMBEDDER=openai              OpenAI, OPENAI_API_KEY, EMBEDDING_MODEL (default text-embedding-3-small)
//	EMBEDDER=ollama              Ollama at OLLAMA_HOST (default http://localhost:11434), EMBEDDING_MODEL (default nomic-embed-text)
//	EMBEDDER=openai-compatible   EMBEDDING_BASE_URL, EMBEDDING_API_KEY, EMBEDDING_MODEL
func FromEnv() (Embedder, error) {
	model := os.Getenv("EMBEDDING_MODEL")
	switch kind := os.Getenv("EMBEDDER"); kind {
	case "", "hash":
		dim := 384
		if v := os.Getenv("EMBEDDING_DIM"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("invalid EMBEDDING_DIM %q", v)
			}
			dim = n
		}
		return NewHashing(dim), nil
	case "openai":
		key := os.Getenv("OPENAI_API_KEY")
		if key == "" {
			return nil, fmt.Errorf("EMBEDDER=openai requires OPENAI_API_KEY")
		}
		if model == "" {
			model = "text-embedding-3-small"
		}
		return NewOpenAI(key, model), nil
	case "ollama":
		host := os.Getenv("OLLAMA_HOST")
		if host == "" {
			host = "http://localhost:11434"
		}
		if model == "" {
			model = "nomic-embed-text"
		}
		return NewOllama(host, model), nil
	case "openai-compatible":
		base := os.Getenv("EMBEDDING_BASE_URL")
		if base == "" || model == "" {
			return nil, fmt.Errorf("EMBEDDER=openai-compatible requires EMBEDDING_BASE_URL and EMBEDDING_MODEL")
		}
		return &OpenAICompatible{BaseURL: base, APIKey: os.Getenv("EMBEDDING_API_KEY"), Model: model}, nil
	default:
		return nil, fmt.Errorf("unknown EMBEDDER %q (want hash, openai, ollama or openai-compatible)", kind)
	}
}
