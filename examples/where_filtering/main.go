// Where filtering: metadata and document filters on get and query.
//
// A Go port of Chroma's where_filtering.ipynb and in_not_in_filtering.ipynb.
//
//	go run ./examples/where_filtering
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
)

func show(label string, v any) {
	b, _ := json.Marshal(v)
	fmt.Printf("%s\n  %s\n\n", label, b)
}

func fresh(ctx context.Context, c *client.Client, name string) *client.Collection {
	if err := c.DeleteCollection(ctx, name); err != nil && !client.IsNotFound(err) {
		exampleenv.Must(err)
	}
	col, err := c.CreateCollection(ctx, name, nil)
	exampleenv.Must(err)
	return col
}

func main() {
	ctx := context.Background()
	c := exampleenv.Client()
	emb := exampleenv.Embedder()

	// --- Filtering on metadata and document contents -------------------
	col := fresh(ctx, c, "filter_example_collection")
	exampleenv.Must(col.Add(ctx, client.Records{
		IDs: []string{"id1", "id2", "id3", "id4", "id5", "id6", "id7", "id8"},
		Embeddings: [][]float32{
			{1.1, 2.3, 3.2}, {4.5, 6.9, 4.4}, {1.1, 2.3, 3.2}, {4.5, 6.9, 4.4},
			{1.1, 2.3, 3.2}, {4.5, 6.9, 4.4}, {1.1, 2.3, 3.2}, {4.5, 6.9, 4.4},
		},
		Metadatas: []client.Metadata{
			{"status": "read"}, {"status": "unread"}, {"status": "read"}, {"status": "unread"},
			{"status": "read"}, {"status": "unread"}, {"status": "read"}, {"status": "unread"},
		},
		Documents: []string{
			"A document that discusses domestic policy", "A document that discusses international affairs",
			"A document that discusses kittens", "A document that discusses dogs",
			"A document that discusses chocolate", "A document that is sixth that discusses government",
			"A document that discusses international affairs", "A document that discusses global affairs",
		},
	}))

	get := func(label string, opts client.GetOptions) {
		res, err := col.Get(ctx, opts)
		exampleenv.Must(err)
		show(label, res.IDs)
	}
	get("Read documents about affairs:", client.GetOptions{
		Where: client.Where{"status": "read"}, WhereDocument: client.WhereDocument{"$contains": "affairs"},
	})
	get("Documents about global affairs or domestic policy:", client.GetOptions{
		WhereDocument: client.WhereDocument{"$or": []any{
			client.WhereDocument{"$contains": "global affairs"},
			client.WhereDocument{"$contains": "domestic policy"},
		}},
	})

	res, err := col.Query(ctx, client.QueryOptions{
		Embeddings: [][]float32{{0, 0, 0}}, NResults: 5,
		WhereDocument: client.WhereDocument{"$contains": "affairs"},
	})
	exampleenv.Must(err)
	show("5 closest to [0,0,0] about affairs (only 3 exist):", res.IDs[0])

	res, err = col.Query(ctx, client.QueryOptions{
		Embeddings: [][]float32{{0, 0, 0}}, NResults: 5,
		WhereDocument: client.WhereDocument{"$not_contains": "domestic policy"},
	})
	exampleenv.Must(err)
	show("5 closest to [0,0,0] not about domestic policy:", res.IDs[0])

	// --- Logical operators: $and / $or (nestable) ----------------------
	articles := fresh(ctx, c, "test-where-list")
	docs := []string{"Article by john", "Article by Jack", "Article by Jill"}
	vecs, err := emb.Embed(ctx, docs)
	exampleenv.Must(err)
	exampleenv.Must(articles.Upsert(ctx, client.Records{
		IDs: []string{"1", "2", "3"}, Embeddings: vecs, Documents: docs,
		Metadatas: []client.Metadata{
			{"author": "john", "category": "chroma", "article_type": "blog"},
			{"author": "jack", "category": "ml", "article_type": "social"},
			{"author": "jill", "category": "lifestyle", "article_type": "paper"},
		},
	}))
	getA := func(label string, opts client.GetOptions) {
		r, err := articles.Get(ctx, opts)
		exampleenv.Must(err)
		show(label, r.Documents)
	}
	getA("$or: john or jack", client.GetOptions{Where: client.Where{"$or": []any{
		client.Where{"author": "john"}, client.Where{"author": "jack"},
	}}})
	getA("$and: chroma category by john", client.GetOptions{Where: client.Where{"$and": []any{
		client.Where{"category": "chroma"}, client.Where{"author": "john"},
	}}})
	getA("$and that matches nothing", client.GetOptions{Where: client.Where{"$and": []any{
		client.Where{"category": "chroma"}, client.Where{"author": "jill"},
	}}})
	getA("Nested $and / $or with a document filter", client.GetOptions{
		WhereDocument: client.WhereDocument{"$contains": "Article"},
		Where: client.Where{"$and": []any{
			client.Where{"category": "chroma"},
			client.Where{"$or": []any{client.Where{"author": "john"}, client.Where{"author": "jack"}}},
		}},
	})

	// --- $in / $nin (in_not_in_filtering.ipynb) --------------------------
	q, err := emb.Embed(ctx, []string{"Give me articles by john"})
	exampleenv.Must(err)
	query := func(label string, where client.Where) {
		r, err := articles.Query(ctx, client.QueryOptions{Embeddings: q, NResults: 10, Where: where})
		exampleenv.Must(err)
		show(label, r.Documents[0])
	}
	query("$in: john or jill", client.Where{"author": client.Where{"$in": []string{"john", "jill"}}})
	query("$nin: neither john nor jill", client.Where{"author": client.Where{"$nin": []string{"john", "jill"}}})
	query("$in combined with $eq", client.Where{"$and": []any{
		client.Where{"author": client.Where{"$in": []string{"john", "jill"}}},
		client.Where{"article_type": client.Where{"$eq": "blog"}},
	}})
	query("$in inside $or", client.Where{"$or": []any{
		client.Where{"author": client.Where{"$in": []string{"john"}}},
		client.Where{"article_type": client.Where{"$in": []string{"paper"}}},
	}})
}
