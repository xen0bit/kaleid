package integration

import (
	"context"
	"testing"

	"github.com/xen0bit/kaleid/internal/auth"
	"github.com/xen0bit/kaleid/pkg/client"
)

// TestGoClient drives the full client surface against an in-process Kaleid.
func TestGoClient(t *testing.T) {
	srv := newServer(t, auth.None{})
	ctx := context.Background()
	c := client.New(srv.URL)

	if _, err := c.Heartbeat(ctx); err != nil {
		t.Fatal(err)
	}
	name := uniq("gocli")
	col, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{
		Metadata: client.Metadata{"owner": "tests"},
		HNSW:     &client.HNSWConfig{Space: client.SpaceCosine, EfSearch: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateCollection(ctx, name, nil); !client.IsConflict(err) {
		t.Fatalf("expected conflict, got %v", err)
	}
	same, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{GetOrCreate: true})
	if err != nil || same.ID != col.ID {
		t.Fatalf("get_or_create: %v %v", same, err)
	}

	err = col.Add(ctx, client.Records{
		IDs:        []string{"a", "b", "c"},
		Embeddings: [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}},
		Documents:  []string{"alpha go", "beta rust", "gamma go"},
		Metadatas:  []client.Metadata{{"n": 1, "f": 2.0, "tags": []string{"x"}}, {"n": 2}, {"n": 3, "g": "grp"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := col.Count(ctx); n != 3 {
		t.Fatalf("count = %d", n)
	}

	got, err := col.Get(ctx, client.GetOptions{IDs: []string{"a"}, Include: []client.Include{client.IncludeMetadatas, client.IncludeEmbeddings}})
	if err != nil {
		t.Fatal(err)
	}
	md := got.Metadatas[0]
	if md["f"].(float64) != 2 || md["n"].(int64) != 1 || md["tags"].([]string)[0] != "x" {
		t.Fatalf("metadata round trip: %#v", md)
	}
	if got.Embeddings[0][0] != 1 {
		t.Fatalf("embedding round trip: %v", got.Embeddings)
	}

	q, err := col.Query(ctx, client.QueryOptions{
		Embeddings: [][]float32{{1, 0}}, NResults: 2,
		WhereDocument: client.WhereDocument{"$contains": "go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(q.IDs[0]) != 2 || q.IDs[0][0] != "a" || q.Distances[0][0] != 0 || *q.Documents[0][1] != "gamma go" {
		t.Fatalf("query: %+v", q)
	}

	if err := col.Update(ctx, client.Records{IDs: []string{"a"}, Metadatas: []client.Metadata{{"f": nil, "new": true}}}); err != nil {
		t.Fatal(err)
	}
	got, _ = col.Get(ctx, client.GetOptions{IDs: []string{"a"}})
	if _, has := got.Metadatas[0]["f"]; has || got.Metadatas[0]["new"] != true {
		t.Fatalf("update merge: %#v", got.Metadatas[0])
	}

	res, err := col.Search(ctx,
		client.Search{
			Where:  client.Where{"n": client.Where{"$gte": 2}},
			Rank:   client.Knn([]float32{1, 0}),
			Limit:  1,
			Select: []string{client.KeyScore, "n"},
		},
		client.Search{Limit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.IDs[0][0] != "c" || res.Scores[0][0] == nil || res.Metadatas[0][0]["n"].(int64) != 3 {
		t.Fatalf("search: %+v", res)
	}
	if len(res.IDs[1]) != 2 || res.Scores[1] != nil {
		t.Fatalf("unranked search: %+v", res)
	}

	fork, err := col.Fork(ctx, name+"-fork")
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := fork.Count(ctx); n != 3 {
		t.Fatalf("fork count = %d", n)
	}
	if st, err := col.IndexingStatus(ctx); err != nil || st.NumUnindexedOps != 0 {
		t.Fatalf("indexing status: %+v %v", st, err)
	}

	newName := name + "-renamed"
	if err := col.Modify(ctx, client.ModifyOptions{Name: &newName, Metadata: client.Metadata{"v": 2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetCollection(ctx, newName); err != nil {
		t.Fatal(err)
	}
	if n, err := col.Delete(ctx, client.DeleteOptions{Where: client.Where{"n": 2}}); err != nil || n != 1 {
		t.Fatalf("delete: %d %v", n, err)
	}
	for _, n := range []string{newName, name + "-fork"} {
		if err := c.DeleteCollection(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.GetCollection(ctx, newName); !client.IsNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestGoClientTokenAuth(t *testing.T) {
	provider, err := auth.NewToken([]auth.TokenEntry{{Token: "t0k", UserID: "u", Tenant: "default_tenant", Databases: []string{"default_database"}}})
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, provider)
	ctx := context.Background()
	if _, err := client.New(srv.URL).ListCollections(ctx, 0, 0); err == nil {
		t.Fatal("expected 401 without a token")
	}
	for _, opt := range []client.Option{client.WithToken("t0k"), client.WithChromaToken("t0k")} {
		id, err := client.New(srv.URL, opt).Identity(ctx)
		if err != nil || id.UserID != "u" {
			t.Fatalf("identity: %+v %v", id, err)
		}
	}
}
