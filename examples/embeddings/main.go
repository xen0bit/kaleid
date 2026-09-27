// Alternative embeddings: the same collection workflow with different
// embedding providers.
//
// A Go port of Chroma's alternative_embeddings.ipynb. Chroma's Python client
// runs embedding functions for you; with Go you embed text yourself and pass
// vectors in. pkg/embed provides embedders for common providers, and
// anything implementing embed.Embedder works.
//
//	go run ./examples/embeddings                        # offline hashing embedder only
//	OPENAI_API_KEY=... go run ./examples/embeddings     # adds OpenAI
//	OLLAMA=1 go run ./examples/embeddings               # adds a local Ollama (nomic-embed-text)
//
// Any OpenAI-compatible embeddings API (vLLM, LM Studio, TEI, Azure
// gateways, ...) works through embed.OpenAICompatible.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

func run(ctx context.Context, c *client.Client, e embed.Embedder) {
	name := "embeddings_" + strings.NewReplacer("/", "-", ":", "-", ".", "-").Replace(e.Name())
	if err := c.DeleteCollection(ctx, name); err != nil && !client.IsNotFound(err) {
		exampleenv.Must(err)
	}
	col, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{
		HNSW: &client.HNSWConfig{Space: client.SpaceCosine},
	})
	exampleenv.Must(err)

	docs := []string{"This is a document", "This is another document"}
	vecs, err := e.Embed(ctx, docs)
	exampleenv.Must(err)
	exampleenv.Must(col.Add(ctx, client.Records{
		IDs: []string{"id1", "id2"}, Embeddings: vecs, Documents: docs,
		Metadatas: []client.Metadata{{"source": "my_source"}, {"source": "my_source"}},
	}))

	q, err := e.Embed(ctx, []string{"This is a query document"})
	exampleenv.Must(err)
	res, err := col.Query(ctx, client.QueryOptions{Embeddings: q, NResults: 2})
	exampleenv.Must(err)
	fmt.Printf("%s (dimension %d):\n", e.Name(), len(vecs[0]))
	for i, id := range res.IDs[0] {
		fmt.Printf("  %s  distance=%.4f  %q\n", id, res.Distances[0][i], *res.Documents[0][i])
	}
}

func main() {
	ctx := context.Background()
	c := exampleenv.Client()

	embedders := []embed.Embedder{embed.NewHashing(384)}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		embedders = append(embedders, embed.NewOpenAI(key, "text-embedding-3-small"))
	}
	if os.Getenv("OLLAMA") != "" {
		host := os.Getenv("OLLAMA_HOST")
		if host == "" {
			host = "http://localhost:11434"
		}
		embedders = append(embedders, embed.NewOllama(host, "nomic-embed-text"))
	}
	for _, e := range embedders {
		run(ctx, c, e)
	}
}
