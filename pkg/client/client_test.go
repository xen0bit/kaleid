package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xen0bit/kaleid/internal/wire"
)

type recorded struct {
	method, path, auth string
	body               map[string]json.RawMessage
}

func fakeServer(t *testing.T, status int, response string) (*httptest.Server, *recorded) {
	t.Helper()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.method, rec.path, rec.auth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			if err := json.Unmarshal(b, &rec.body); err != nil {
				t.Errorf("request body is not a JSON object: %s", b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func testCollection(base string, opts ...Option) *Collection {
	c := New(base, opts...)
	return &Collection{ID: "c1", Name: "docs", Tenant: DefaultTenant, Database: DefaultDatabase, c: c}
}

func TestAddEncodesFloatsAndBase64(t *testing.T) {
	srv, rec := fakeServer(t, 201, `{}`)
	col := testCollection(srv.URL, WithToken("tok"))
	err := col.Add(context.Background(), Records{
		IDs:        []string{"a"},
		Embeddings: [][]float32{{0.25, 0.75}},
		Metadatas:  []Metadata{{"f": 3.0, "i": 3, "s": "x", "sv": SparseVector{Indices: []uint32{1}, Values: []float32{0.5}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rec.path != "/api/v2/tenants/default_tenant/databases/default_database/collections/c1/add" || rec.auth != "Bearer tok" {
		t.Fatalf("unexpected request %s auth=%q", rec.path, rec.auth)
	}
	if got := string(rec.body["embeddings"]); got != `["AACAPgAAQD8="]` {
		t.Fatalf("embeddings = %s", got)
	}
	var mds []map[string]json.RawMessage
	_ = json.Unmarshal(rec.body["metadatas"], &mds)
	if string(mds[0]["f"]) != "3.0" || string(mds[0]["i"]) != "3" {
		t.Fatalf("int/float not preserved: %s", rec.body["metadatas"])
	}
	if !strings.Contains(string(mds[0]["sv"]), `"#type":"sparse_vector"`) {
		t.Fatalf("sparse vector encoding: %s", mds[0]["sv"])
	}
}

func TestRecordLengthValidation(t *testing.T) {
	col := testCollection("http://unused")
	err := col.Add(context.Background(), Records{IDs: []string{"a", "b"}, Embeddings: [][]float32{{1}}})
	if err == nil || !strings.Contains(err.Error(), "Embeddings has 1 entries") {
		t.Fatalf("expected length error, got %v", err)
	}
}

func TestErrorDecoding(t *testing.T) {
	srv, _ := fakeServer(t, 404, `{"error":"NotFoundError","message":"Collection [x] does not exist"}`)
	_, err := New(srv.URL).GetCollection(context.Background(), "x")
	if !IsNotFound(err) || IsConflict(err) {
		t.Fatalf("expected not found, got %v", err)
	}
	var e *Error
	if !errorsAs(err, &e) || e.Name != "NotFoundError" || e.Message != "Collection [x] does not exist" {
		t.Fatalf("unexpected error %#v", err)
	}
}

func errorsAs(err error, target **Error) bool {
	e, ok := err.(*Error)
	if ok {
		*target = e
	}
	return ok
}

func TestGetDecodesTypedMetadata(t *testing.T) {
	srv, rec := fakeServer(t, 200, `{"ids":["a"],"embeddings":[[1.0,0.5]],"documents":[null],"uris":null,
		"metadatas":[{"f":3.0,"i":3,"arr":["x"],"sv":{"#type":"sparse_vector","indices":[2],"values":[1.0]}}],"include":["documents","metadatas","embeddings"]}`)
	res, err := testCollection(srv.URL).Get(context.Background(), GetOptions{
		Where: Where{"i": Where{"$gte": 1}}, Include: []Include{IncludeDocuments, IncludeMetadatas, IncludeEmbeddings},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(rec.body["where"]) != `{"i":{"$gte":1}}` {
		t.Fatalf("where = %s", rec.body["where"])
	}
	md := res.Metadatas[0]
	if _, ok := md["f"].(float64); !ok {
		t.Fatalf("f decoded as %T", md["f"])
	}
	if _, ok := md["i"].(int64); !ok {
		t.Fatalf("i decoded as %T", md["i"])
	}
	if sv, ok := md["sv"].(*SparseVector); !ok || sv.Indices[0] != 2 {
		t.Fatalf("sv decoded as %#v", md["sv"])
	}
	if res.Documents[0] != nil || res.URIs != nil || res.Embeddings[0][1] != 0.5 {
		t.Fatalf("unexpected columns %+v", res)
	}
}

func TestRRFExpression(t *testing.T) {
	r := RRF(60, Knn([]float32{1}, KnnOptions{ReturnRank: true}), KnnSparse(SparseVector{Indices: []uint32{1}, Values: []float32{1}}, "sp", KnnOptions{ReturnRank: true}))
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var round map[string]any
	_ = json.Unmarshal(b, &round)
	s := string(b)
	for _, want := range []string{`"$mul"`, `"$val":-1`, `"$div"`, `"return_rank":true`, `"key":"sp"`, `"#type":"sparse_vector"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("RRF JSON missing %s: %s", want, s)
		}
	}
}

func TestMetadataRejectsUnsupportedValues(t *testing.T) {
	if _, err := json.Marshal(Metadata{"bad": struct{}{}}); err == nil {
		t.Fatal("expected error for unsupported type")
	}
	if _, err := toWireValue(wire.Value{}); err == nil {
		t.Fatal("expected error for foreign type")
	}
}
