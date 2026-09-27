package main

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

func TestEvaluateMatchesTrecEval(t *testing.T) {
	// q1: relevant doc at rank 2; q2: relevant doc not retrieved.
	ranked := map[string][]string{"q1": {"x", "d1", "y"}, "q2": {"a", "b", "c"}}
	relevant := map[string]map[string]bool{"q1": {"d1": true}, "q2": {"d2": true}}
	m := Evaluate(ranked, relevant, []int{1, 3})
	// q1: P@1=0, P@3=1/3, R@3=1, AP@3=1/2, NDCG@3=1/log2(3); q2: all zero.
	if !near(m.Precision["P@1"], 0) || !near(m.Precision["P@3"], 0.16667) {
		t.Fatalf("precision %v", m.Precision)
	}
	if !near(m.Recall["Recall@3"], 0.5) || !near(m.MAP["MAP@3"], 0.25) {
		t.Fatalf("recall/map %v %v", m.Recall, m.MAP)
	}
	if !near(m.NDCG["NDCG@3"], math.Round(0.5/math.Log2(3)*1e5)/1e5) {
		t.Fatalf("ndcg %v", m.NDCG)
	}
}

func TestHeuristicQuery(t *testing.T) {
	q := heuristicQuery("Collections store embeddings. Query collections with query embeddings and filters.")
	if q == "" || q[:11] != "collections" {
		t.Fatalf("got %q", q)
	}
}

// TestBenchmarkPipeline runs evaluate end to end on a slice of the corpus
// with heuristic queries against a live server (KALEID_URL).
func TestBenchmarkPipeline(t *testing.T) {
	base := os.Getenv("KALEID_URL")
	if base == "" {
		t.Skip("KALEID_URL not set")
	}
	var corpus map[string]string
	readJSON("data/chroma_docs.json", &corpus)
	var queries []Query
	for _, id := range sortedIDs(corpus)[:60] {
		if q := heuristicQuery(corpus[id]); q != "" {
			queries = append(queries, Query{ID: id, Query: q})
		}
	}
	m, err := runBenchmark(context.Background(), client.New(base), embed.NewHashing(384), corpus, queries, "gb-test")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("heuristic queries with hash-384: %+v", m)
	if m.Recall["Recall@10"] < 0.5 {
		t.Fatalf("recall@10 unexpectedly low: %v", m.Recall)
	}
	_ = client.New(base).DeleteCollection(context.Background(), "gb-test")
}
