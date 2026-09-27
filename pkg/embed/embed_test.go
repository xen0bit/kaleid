package embed

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i] * b[i])
		na += float64(a[i] * a[i])
		nb += float64(b[i] * b[i])
	}
	return dot / math.Sqrt(na*nb)
}

func TestHashingIsDeterministicAndLexical(t *testing.T) {
	h := NewHashing(256)
	v, _ := h.Embed(context.Background(), []string{"postgres vector search", "vector search in postgres", "baking sourdough bread", ""})
	again, _ := h.Embed(context.Background(), []string{"postgres vector search"})
	for i := range v[0] {
		if v[0][i] != again[0][i] {
			t.Fatal("hashing embedder is not deterministic")
		}
	}
	if near, far := cosine(v[0], v[1]), cosine(v[0], v[2]); near <= far {
		t.Fatalf("expected related texts to be closer: near=%v far=%v", near, far)
	}
	if math.Abs(cosine(v[0], v[0])-1) > 1e-5 || v[3][0] != 1 {
		t.Fatal("vectors must be unit length, empty text a fixed vector")
	}
}

func TestBM25Encoding(t *testing.T) {
	b := NewBM25()
	doc := b.EncodeDocument("The quick fox jumps over the lazy fox")
	for i := 1; i < len(doc.Indices); i++ {
		if doc.Indices[i] <= doc.Indices[i-1] {
			t.Fatal("indices must be strictly ascending")
		}
	}
	var foxW, quickW float32
	for i, tok := range doc.Tokens {
		switch tok {
		case "fox":
			foxW = doc.Values[i]
		case "quick":
			quickW = doc.Values[i]
		case "the":
			t.Fatal("stopwords must be dropped")
		}
	}
	if foxW <= quickW {
		t.Fatalf("repeated term should weigh more: fox=%v quick=%v", foxW, quickW)
	}
	q := b.EncodeQuery("fox fox")
	if len(q.Values) != 1 || q.Values[0] != 1 {
		t.Fatalf("query weights: %+v", q)
	}
}

func TestOpenAICompatible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer k" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var req struct {
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		type item struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		// Return out of order to check re-sorting by index.
		data := []item{}
		for i := len(req.Input) - 1; i >= 0; i-- {
			data = append(data, item{i, []float32{float32(i)}})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	defer srv.Close()
	e := &OpenAICompatible{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "m", BatchSize: 2}
	v, err := e.Embed(context.Background(), []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 3 || v[0][0] != 0 || v[1][0] != 1 || v[2][0] != 0 {
		t.Fatalf("got %v", v)
	}
}
