package compat

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/xen0bit/kaleid/pkg/client"
)

// clientFlow runs the OSS subset of the Go client against one server and
// returns everything it observed. It avoids array metadata: Chroma 1.5.9
// leaves array values behind when a collection is deleted, and they then
// attach to unrelated records in later collections.
func clientFlow(t *testing.T, base string) map[string]any {
	t.Helper()
	ctx := context.Background()
	c := client.New(base)
	name := fmt.Sprintf("goclient%d", time.Now().UnixNano())
	col, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{
		Metadata: client.Metadata{"k": 1.0},
		HNSW:     &client.HNSWConfig{Space: client.SpaceIP},
	})
	if err != nil {
		t.Fatalf("%s: create: %v", base, err)
	}
	defer c.DeleteCollection(ctx, name) //nolint:errcheck
	err = col.Add(ctx, client.Records{
		IDs:        []string{"a", "b", "c", "d"},
		Embeddings: [][]float32{{1, 0}, {0, 1}, {0.5, 0.5}, {2, 1}},
		Documents:  []string{"one", "two", "three", "four"},
		URIs:       []string{"u1", "u2", "u3", "u4"},
		Metadatas:  []client.Metadata{{"n": 1, "f": 1.5}, {"n": 2, "s": "x"}, {"n": 3}, {"n": 4, "b": true}},
	})
	if err != nil {
		t.Fatalf("%s: add: %v", base, err)
	}
	if err := col.Upsert(ctx, client.Records{IDs: []string{"a", "e"}, Embeddings: [][]float32{{1, 1}, {3, 0}}, Metadatas: []client.Metadata{{"f": nil, "u": 1}, {"n": 5}}}); err != nil {
		t.Fatalf("%s: upsert: %v", base, err)
	}
	out := map[string]any{}
	get, err := col.Get(ctx, client.GetOptions{Include: []client.Include{client.IncludeDocuments, client.IncludeMetadatas, client.IncludeEmbeddings, client.IncludeURIs}})
	if err != nil {
		t.Fatalf("%s: get: %v", base, err)
	}
	out["get"] = get
	q, err := col.Query(ctx, client.QueryOptions{Embeddings: [][]float32{{1, 0}, {0, 1}}, NResults: 3, Where: client.Where{"n": client.Where{"$ne": 3}}})
	if err != nil {
		t.Fatalf("%s: query: %v", base, err)
	}
	out["query"] = q
	n, err := col.Delete(ctx, client.DeleteOptions{Where: client.Where{"n": client.Where{"$in": []int{2, 4}}}})
	if err != nil {
		t.Fatalf("%s: delete: %v", base, err)
	}
	out["deleted"] = n
	out["count"], _ = col.Count(ctx)
	_, err = c.GetCollection(ctx, name+"-missing")
	out["missing_is_404"] = client.IsNotFound(err)
	fetched, err := c.GetCollection(ctx, name)
	if err != nil {
		t.Fatalf("%s: get collection: %v", base, err)
	}
	out["collection_metadata"] = fetched.Metadata
	out["dimension"] = *fetched.Dimension
	return out
}

// TestGoClientAgainstBothServers checks that the Go client observes the same
// results from Chroma and Kaleid.
func TestGoClientAgainstBothServers(t *testing.T) {
	ref, kal := os.Getenv("CHROMA_URL"), os.Getenv("KALEID_URL")
	if ref == "" || kal == "" {
		t.Skip("CHROMA_URL and KALEID_URL must be set")
	}
	a, b := clientFlow(t, ref), clientFlow(t, kal)
	for k := range a {
		if !reflect.DeepEqual(a[k], b[k]) {
			t.Errorf("%s differs:\n chroma: %#v\n kaleid: %#v", k, a[k], b[k])
		}
	}
}
