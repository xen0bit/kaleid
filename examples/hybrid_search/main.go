// Hybrid search: combine dense (semantic) and BM25 (keyword) rankings with
// reciprocal rank fusion, then group results by a metadata field.
//
// This uses the Search API, which Chroma serves only in Chroma Cloud and
// Kaleid serves everywhere. BM25 works in two halves: each document carries a
// sparse vector of saturated term frequencies (embed.BM25), and because the
// collection schema marks that key "bm25": true, the server applies inverse
// document frequency at query time.
//
//	go run ./examples/hybrid_search
package main

import (
	"context"
	"fmt"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

var docs = []struct{ id, product, text string }{
	{"pg-1", "postgres", "PostgreSQL supports streaming replication and point-in-time recovery for backups."},
	{"pg-2", "postgres", "Use VACUUM and autovacuum settings to control table bloat after heavy updates."},
	{"pg-3", "postgres", "The pgvector extension adds a vector type and HNSW indexes for similarity search."},
	{"go-1", "go", "Go's context package carries deadlines and cancellation across API boundaries."},
	{"go-2", "go", "Use errors.Is and errors.As to inspect wrapped errors instead of comparing strings."},
	{"go-3", "go", "The net/http server handles each request in its own goroutine."},
	{"k8s-1", "kubernetes", "A Deployment manages replica sets and rolling updates of stateless pods."},
	{"k8s-2", "kubernetes", "Readiness probes keep traffic away from pods that are not ready to serve."},
	{"k8s-3", "kubernetes", "PersistentVolumeClaims request durable storage for stateful workloads like databases."},
}

func main() {
	ctx := context.Background()
	c := exampleenv.Client()
	dense := exampleenv.Embedder()
	bm25 := embed.NewBM25()

	const name, sparseKey = "hybrid_search_demo", "bm25"
	if err := c.DeleteCollection(ctx, name); err != nil && !client.IsNotFound(err) {
		exampleenv.Must(err)
	}
	col, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{
		Schema: embed.BM25Schema(sparseKey),
	})
	exampleenv.Must(err)

	var ids, texts []string
	var mds []client.Metadata
	for _, d := range docs {
		ids = append(ids, d.id)
		texts = append(texts, d.text)
		mds = append(mds, client.Metadata{"product": d.product, sparseKey: bm25.EncodeDocument(d.text)})
	}
	vecs, err := dense.Embed(ctx, texts)
	exampleenv.Must(err)
	exampleenv.Must(col.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: texts, Metadatas: mds}))

	query := "database backups and replication"
	qvec, err := dense.Embed(ctx, []string{query})
	exampleenv.Must(err)
	qsparse := bm25.EncodeQuery(query)

	// Each ranking contributes its top 50. A record missing from one ranking
	// gets that ranking's Default (here: rank 50, i.e. last) instead of being
	// dropped, so the fusion is over the union of both result lists.
	const limit = 50
	last := float64(limit)
	denseRank := client.Knn(qvec[0], client.KnnOptions{Limit: limit, ReturnRank: true, Default: &last})
	sparseRank := client.KnnSparse(qsparse, sparseKey, client.KnnOptions{Limit: limit, ReturnRank: true, Default: &last})
	sel := []string{client.KeyDocument, client.KeyScore, "product"}

	res, err := col.Search(ctx,
		client.Search{Rank: client.Knn(qvec[0]), Limit: 3, Select: sel},
		client.Search{Rank: client.KnnSparse(qsparse, sparseKey), Limit: 3, Select: sel},
		client.Search{Rank: client.RRF(60, denseRank, sparseRank), Limit: 3, Select: sel},
		// Best hit per product among the fused results.
		client.Search{
			Rank:    client.RRF(60, denseRank, sparseRank),
			GroupBy: &client.GroupBy{Keys: []string{"product"}, Aggregate: client.MinK(1, client.KeyScore)},
			Select:  sel,
		},
		// Filters apply before ranking.
		client.Search{
			Where:  client.Where{"product": client.Where{"$ne": "postgres"}},
			Rank:   client.RRF(60, denseRank, sparseRank),
			Limit:  3,
			Select: sel,
		},
	)
	exampleenv.Must(err)

	labels := []string{"dense only", "BM25 only", "hybrid (RRF)", "hybrid, best per product", "hybrid, excluding postgres"}
	fmt.Printf("Query: %q\n", query)
	for i, label := range labels {
		fmt.Printf("\n%s:\n", label)
		for j, id := range res.IDs[i] {
			fmt.Printf("  %-6s %-10s score=%8.4f  %s\n", id, res.Metadatas[i][j]["product"], *res.Scores[i][j], *res.Documents[i][j])
		}
	}
}
