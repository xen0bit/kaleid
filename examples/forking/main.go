// Forking: branch a collection, change the branch, leave the original intact.
//
// A Go port of Chroma's advanced/forking.ipynb. In Chroma, forking is a
// Chroma Cloud feature; Kaleid supports it on any deployment. A fork is an
// independent copy made in one transaction, so changes to either side never
// affect the other.
//
//	go run ./examples/forking
package main

import (
	"context"
	"fmt"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
)

func main() {
	ctx := context.Background()
	c := exampleenv.Client()
	emb := exampleenv.Embedder()

	for _, name := range []string{"main-repo-index", "main-repo-index-pr-1234"} {
		if err := c.DeleteCollection(ctx, name); err != nil && !client.IsNotFound(err) {
			exampleenv.Must(err)
		}
	}
	source, err := c.CreateCollection(ctx, "main-repo-index", nil)
	exampleenv.Must(err)

	// Index the "main branch" of a code base.
	files := map[string]string{
		"src/server.go": "HTTP server with routes for collections and records",
		"src/store.go":  "PostgreSQL storage layer with per-collection tables",
		"README.md":     "Project overview and quickstart instructions",
	}
	var ids, docs []string
	for path, summary := range files {
		ids = append(ids, path)
		docs = append(docs, summary)
	}
	vecs, err := emb.Embed(ctx, docs)
	exampleenv.Must(err)
	exampleenv.Must(source.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: docs}))

	// Fork for a pull request and apply the PR's changes to the fork only.
	pr, err := source.Fork(ctx, "main-repo-index-pr-1234")
	exampleenv.Must(err)
	newDoc := []string{"Search API handler with hybrid ranking"}
	newVec, err := emb.Embed(ctx, newDoc)
	exampleenv.Must(err)
	exampleenv.Must(pr.Add(ctx, client.Records{IDs: []string{"src/search.go"}, Embeddings: newVec, Documents: newDoc}))
	_, err = pr.Delete(ctx, client.DeleteOptions{IDs: []string{"README.md"}})
	exampleenv.Must(err)

	for _, col := range []*client.Collection{source, pr} {
		res, err := col.Get(ctx, client.GetOptions{Include: []client.Include{}})
		exampleenv.Must(err)
		fmt.Printf("%-26s %d records: %v\n", col.Name, len(res.IDs), res.IDs)
	}

	q, err := emb.Embed(ctx, []string{"hybrid search ranking"})
	exampleenv.Must(err)
	for _, col := range []*client.Collection{source, pr} {
		res, err := col.Query(ctx, client.QueryOptions{Embeddings: q, NResults: 1})
		exampleenv.Must(err)
		fmt.Printf("best match in %-26s %s\n", col.Name+":", res.IDs[0][0])
	}
}
