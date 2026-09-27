// Package integration exercises Kaleid end to end against a real PostgreSQL
// with pgvector, covering features that have no local Chroma reference
// (Search API, sparse/BM25, fork, auth, high-dimension indexes).
//
// Run with KALEID_TEST_DATABASE_URL=postgres://... go test ./test/integration
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xen0bit/kaleid/internal/api"
	"github.com/xen0bit/kaleid/internal/auth"
	"github.com/xen0bit/kaleid/internal/store"
)

func newServer(t *testing.T, provider auth.Provider) *httptest.Server {
	t.Helper()
	url := os.Getenv("KALEID_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KALEID_TEST_DATABASE_URL not set")
	}
	st, err := store.Open(context.Background(), url, store.Options{ExactFallback: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := httptest.NewServer(api.New(st, provider, api.Config{}, log).Handler())
	t.Cleanup(srv.Close)
	return srv
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c *client) do(method, path string, body any) (int, map[string]any, []byte) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.base+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m, raw
}

func (c *client) must(method, path string, body any) map[string]any {
	c.t.Helper()
	code, m, raw := c.do(method, path, body)
	if code >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, code, raw)
	}
	return m
}

const base = "/api/v2/tenants/default_tenant/databases/default_database/collections"

func uniq(prefix string) string { return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()) }

func (c *client) createCollection(body map[string]any) string {
	m := c.must("POST", base, body)
	return m["id"].(string)
}

func ids(m map[string]any, i int) []string {
	arr := m["ids"].([]any)[i].([]any)
	out := make([]string, len(arr))
	for j, v := range arr {
		out[j] = v.(string)
	}
	return out
}

func TestSearchDenseFilterSelect(t *testing.T) {
	srv := newServer(t, auth.None{})
	c := &client{t: t, base: srv.URL}
	id := c.createCollection(map[string]any{"name": uniq("srch")})
	c.must("POST", base+"/"+id+"/add", map[string]any{
		"ids":        []string{"a", "b", "c", "d"},
		"embeddings": [][]float32{{1, 0}, {0, 1}, {0.6, 0.8}, {0.9, 0.1}},
		"documents":  []string{"alpha doc", "beta doc", "gamma", "delta doc"},
		"metadatas":  []map[string]any{{"g": "x", "n": 1}, {"g": "y", "n": 2}, {"g": "x", "n": 3}, {"g": "y", "n": 4}},
	})
	res := c.must("POST", base+"/"+id+"/search", map[string]any{"searches": []any{
		map[string]any{
			"filter": map[string]any{"$or": []any{map[string]any{"g": "x"}, map[string]any{"n": map[string]any{"$gte": 4}}}},
			"rank":   map[string]any{"$knn": map[string]any{"query": []float32{1, 0}}},
			"limit":  map[string]any{"limit": 2},
			"select": map[string]any{"keys": []string{"n", "#score", "#document"}},
		},
		map[string]any{"limit": map[string]any{"limit": 2, "offset": 1}},
	}})
	if got := ids(res, 0); strings.Join(got, ",") != "a,d" {
		t.Fatalf("ranked ids %v", got)
	}
	if got := ids(res, 1); strings.Join(got, ",") != "b,c" {
		t.Fatalf("unranked ids %v", got)
	}
	sel := res["select"].([]any)[0].([]any)
	if fmt.Sprint(sel) != "[#document #score n]" {
		t.Fatalf("select %v", sel)
	}
	md := res["metadatas"].([]any)[0].([]any)[0].(map[string]any)
	if len(md) != 1 || md["n"].(float64) != 1 {
		t.Fatalf("projected metadata %v", md)
	}
	if res["scores"].([]any)[1] != nil || res["embeddings"].([]any)[0] != nil {
		t.Fatalf("unselected columns must be null: %s", mustJSON(res))
	}
	if s := res["scores"].([]any)[0].([]any)[0].(float64); s != 0 {
		t.Fatalf("expected l2 distance 0 for exact match, got %v", s)
	}
}

func TestSearchRRFAndGroupBy(t *testing.T) {
	srv := newServer(t, auth.None{})
	c := &client{t: t, base: srv.URL}
	id := c.createCollection(map[string]any{"name": uniq("rrf"), "schema": map[string]any{
		"defaults": map[string]any{},
		"keys": map[string]any{"sparse": map[string]any{"sparse_vector": map[string]any{
			"sparse_vector_index": map[string]any{"enabled": true, "config": map[string]any{"bm25": true}},
		}}},
	}})
	sv := func(idx []int, vals []float32) map[string]any {
		return map[string]any{"#type": "sparse_vector", "indices": idx, "values": vals}
	}
	c.must("POST", base+"/"+id+"/add", map[string]any{
		"ids":        []string{"a", "b", "c"},
		"embeddings": [][]float32{{1, 0}, {0, 1}, {0.7, 0.7}},
		"metadatas": []map[string]any{
			{"g": "x", "sparse": sv([]int{1, 2}, []float32{1, 1})},
			{"g": "y", "sparse": sv([]int{2}, []float32{1})},
			{"g": "x", "sparse": sv([]int{3}, []float32{1})},
		},
	})
	knn := func(q any, key string) map[string]any {
		return map[string]any{"$knn": map[string]any{"query": q, "key": key, "return_rank": true}}
	}
	rrf := map[string]any{"$mul": []any{
		map[string]any{"$val": -1},
		map[string]any{"$sum": []any{
			map[string]any{"$div": map[string]any{"left": map[string]any{"$val": 1}, "right": map[string]any{"$sum": []any{map[string]any{"$val": 60}, knn([]float32{1, 0}, "#embedding")}}}},
			map[string]any{"$div": map[string]any{"left": map[string]any{"$val": 1}, "right": map[string]any{"$sum": []any{map[string]any{"$val": 60}, knn(map[string]any{"indices": []int{1}, "values": []float32{1}}, "sparse")}}}},
		}},
	}}
	res := c.must("POST", base+"/"+id+"/search", map[string]any{"searches": []any{
		map[string]any{"rank": rrf, "select": map[string]any{"keys": []string{"#score"}}},
		map[string]any{
			"rank":     map[string]any{"$knn": map[string]any{"query": []float32{1, 0}}},
			"group_by": map[string]any{"keys": []string{"g"}, "aggregate": map[string]any{"$min_k": map[string]any{"keys": []string{"#score"}, "k": 1}}},
		},
		map[string]any{"rank": map[string]any{"$knn": map[string]any{"query": map[string]any{"indices": []int{2}, "values": []float32{1}}, "key": "sparse"}}},
	}})
	// Only "a" appears in both KNN results (no defaults), so RRF keeps just it.
	if got := ids(res, 0); strings.Join(got, ",") != "a" {
		t.Fatalf("rrf ids %v", got)
	}
	// Best record per group: a (x) then b (y).
	if got := ids(res, 1); strings.Join(got, ",") != "a,b" {
		t.Fatalf("group_by ids %v", got)
	}
	// Sparse KNN on term 2: a and b both contain it; b is shorter but BM25
	// here only rescales the query, so both get the same score and tie-break
	// by insertion order.
	if got := ids(res, 2); strings.Join(got, ",") != "a,b" {
		t.Fatalf("sparse ids %v", got)
	}
	code, _, raw := c.do("POST", base+"/"+id+"/search", map[string]any{"searches": []any{
		map[string]any{"group_by": map[string]any{"keys": []string{"g"}, "aggregate": map[string]any{"$min_k": map[string]any{"keys": []string{"#score"}, "k": 1}}}},
	}})
	if code != 400 || !strings.Contains(string(raw), "group_by requires rank") {
		t.Fatalf("expected group_by validation error, got %d %s", code, raw)
	}
}

func TestForkAndIndexingStatus(t *testing.T) {
	srv := newServer(t, auth.None{})
	c := &client{t: t, base: srv.URL}
	name := uniq("fork")
	id := c.createCollection(map[string]any{"name": name, "metadata": map[string]any{"hnsw:space": "cosine"}})
	c.must("POST", base+"/"+id+"/add", map[string]any{"ids": []string{"a", "b"}, "embeddings": [][]float32{{1, 0}, {0, 1}}})
	fork := c.must("POST", base+"/"+id+"/fork", map[string]any{"new_name": name + "-f"})
	fid := fork["id"].(string)
	c.must("POST", base+"/"+fid+"/add", map[string]any{"ids": []string{"c"}, "embeddings": [][]float32{{1, 1}}})
	for _, tc := range []struct {
		id   string
		want float64
	}{{id, 2}, {fid, 3}} {
		code, _, raw := c.do("GET", base+"/"+tc.id+"/count", nil)
		if code != 200 || strings.TrimSpace(string(raw)) != fmt.Sprint(tc.want) {
			t.Fatalf("count %s: %d %s", tc.id, code, raw)
		}
	}
	q := c.must("POST", base+"/"+fid+"/query", map[string]any{"query_embeddings": [][]float32{{1, 0}}, "n_results": 3})
	if got := ids(q, 0); strings.Join(got, ",") != "a,c,b" {
		t.Fatalf("fork query %v", got)
	}
	fc := c.must("GET", base+"/"+id+"/fork_count", nil)
	if fc["count"].(float64) != 1 {
		t.Fatalf("fork_count %v", fc)
	}
	st := c.must("GET", base+"/"+id+"/indexing_status", nil)
	if st["num_unindexed_ops"].(float64) != 0 || st["op_indexing_progress"].(float64) != 1 {
		t.Fatalf("indexing_status %v", st)
	}
}

func TestTokenAuth(t *testing.T) {
	provider, err := auth.NewToken([]auth.TokenEntry{
		{Token: "admin-token"},
		{Token: "tenant-token", UserID: "u1", Tenant: "default_tenant", Databases: []string{"default_database"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer(t, provider)
	anon := &client{t: t, base: srv.URL}
	if code, _, _ := anon.do("GET", "/api/v2/heartbeat", nil); code != 200 {
		t.Fatal("heartbeat must not require auth")
	}
	if code, m, _ := anon.do("GET", base, nil); code != 401 || m["error"] != "AuthError" {
		t.Fatalf("expected 401, got %d %v", code, m)
	}
	scoped := &client{t: t, base: srv.URL, token: "tenant-token"}
	id := scoped.must("GET", "/api/v2/auth/identity", nil)
	if id["user_id"] != "u1" || id["tenant"] != "default_tenant" {
		t.Fatalf("identity %v", id)
	}
	scoped.must("GET", base, nil)
	if code, _, _ := scoped.do("GET", "/api/v2/tenants/default_tenant/databases/otherdb/collections", nil); code != 403 {
		t.Fatalf("expected 403 for other database, got %d", code)
	}
	req, _ := http.NewRequest("GET", srv.URL+base, nil)
	req.Header.Set("X-Chroma-Token", "admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("X-Chroma-Token admin access failed: %v %v", resp.StatusCode, err)
	}
	resp.Body.Close()
}

func randVec(r *rand.Rand, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = r.Float32()*2 - 1
	}
	return v
}

func l2sq(a, b []float32) float64 {
	var s float64
	for i := range a {
		d := float64(a[i] - b[i])
		s += d * d
	}
	return s
}

// TestFilteredRecallIsExact checks that a highly selective filter still
// returns exactly the true nearest neighbours (iterative scan + exact
// fallback), including a halfvec-indexed collection above 2000 dimensions.
func TestFilteredRecallIsExact(t *testing.T) {
	srv := newServer(t, auth.None{})
	c := &client{t: t, base: srv.URL}
	for _, dim := range []int{16, 2048} {
		r := rand.New(rand.NewSource(int64(dim)))
		id := c.createCollection(map[string]any{"name": uniq(fmt.Sprintf("recall%d", dim))})
		const n = 600
		vecs := make([][]float32, n)
		idsIn := make([]string, n)
		mds := make([]map[string]any, n)
		for i := range vecs {
			vecs[i] = randVec(r, dim)
			idsIn[i] = fmt.Sprintf("r%d", i)
			mds[i] = map[string]any{"bucket": i % 50}
		}
		c.must("POST", base+"/"+id+"/add", map[string]any{"ids": idsIn, "embeddings": vecs, "metadatas": mds})
		q := randVec(r, dim)
		res := c.must("POST", base+"/"+id+"/query", map[string]any{
			"query_embeddings": [][]float32{q}, "n_results": 10,
			"where": map[string]any{"bucket": 7}, "include": []string{"distances"},
		})
		got := ids(res, 0)
		type cand struct {
			id string
			d  float64
		}
		var want []cand
		for i := range vecs {
			if i%50 == 7 {
				want = append(want, cand{idsIn[i], l2sq(vecs[i], q)})
			}
		}
		sort.Slice(want, func(i, j int) bool { return want[i].d < want[j].d })
		if len(got) != 10 {
			t.Fatalf("dim %d: expected 10 results, got %d", dim, len(got))
		}
		for i := range got {
			if got[i] != want[i].id {
				t.Fatalf("dim %d: result %d = %s, want %s", dim, i, got[i], want[i].id)
			}
			d := res["distances"].([]any)[0].([]any)[i].(float64)
			if math.Abs(d-want[i].d) > 1e-3*math.Max(1, want[i].d) {
				t.Fatalf("dim %d: distance %v, want %v", dim, d, want[i].d)
			}
		}
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
