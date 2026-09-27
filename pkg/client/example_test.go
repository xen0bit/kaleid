package client_test

import (
	"context"
	"fmt"
	"log"

	"github.com/xen0bit/kaleid/pkg/client"
)

func Example() {
	ctx := context.Background()
	c := client.New("http://localhost:8000")

	col, err := c.CreateCollection(ctx, "articles", &client.CreateCollectionOptions{
		HNSW:        &client.HNSWConfig{Space: client.SpaceCosine},
		GetOrCreate: true,
	})
	if err != nil {
		log.Fatal(err)
	}
	err = col.Upsert(ctx, client.Records{
		IDs:        []string{"a1", "a2"},
		Embeddings: [][]float32{{0.1, 0.9}, {0.8, 0.2}},
		Documents:  []string{"Postgres tips", "Vector search basics"},
		Metadatas:  []client.Metadata{{"year": 2024}, {"year": 2026}},
	})
	if err != nil {
		log.Fatal(err)
	}
	res, err := col.Query(ctx, client.QueryOptions{
		Embeddings: [][]float32{{0.7, 0.3}},
		NResults:   1,
		Where:      client.Where{"year": client.Where{"$gte": 2025}},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.IDs[0])
}

func ExampleCollection_Search() {
	ctx := context.Background()
	col, err := client.New("http://localhost:8000").GetCollection(ctx, "articles")
	if err != nil {
		log.Fatal(err)
	}
	// Hybrid search: fuse a dense and a sparse (e.g. BM25) ranking.
	res, err := col.Search(ctx, client.Search{
		Rank: client.RRF(60,
			client.Knn([]float32{0.7, 0.3}, client.KnnOptions{ReturnRank: true}),
			client.KnnSparse(client.SparseVector{Indices: []uint32{17}, Values: []float32{1}}, "bm25", client.KnnOptions{ReturnRank: true}),
		),
		Limit:  5,
		Select: []string{client.KeyDocument, client.KeyScore},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(res.IDs[0])
}
