package search

import (
	"encoding/json"
	"testing"

	"github.com/xen0bit/kaleid/internal/wire"
)

func ms(pairs ...float32) []Measure {
	out := make([]Measure, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, Measure{RowID: int64(pairs[i]), Score: pairs[i+1]})
	}
	return out
}

func parseRank(t *testing.T, s string) *Expr {
	t.Helper()
	e, err := parseExpr(json.RawMessage(s))
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEvaluateKnnAndArithmetic(t *testing.T) {
	e := parseRank(t, `{"$sum":[{"$mul":[{"$knn":{"query":[1,0]}},{"$val":0.5}]},{"$val":1}]}`)
	got := Evaluate(e, [][]Measure{ms(1, 0.2, 2, 0.8)})
	if len(got) != 2 || got[0].RowID != 1 || got[0].Score != 1.1 || got[1].Score != 1.4 {
		t.Fatalf("got %+v", got)
	}
}

func TestEvaluateDefaultsAndIntersection(t *testing.T) {
	// No defaults: only records present in both KNN results survive.
	e := parseRank(t, `{"$sum":[{"$knn":{"query":[1]}},{"$knn":{"query":[1]}}]}`)
	got := Evaluate(e, [][]Measure{ms(1, 1, 2, 2), ms(2, 3, 3, 4)})
	if len(got) != 1 || got[0].RowID != 2 || got[0].Score != 5 {
		t.Fatalf("intersection: %+v", got)
	}
	// With a default on the second leaf, the union of the first is kept.
	e = parseRank(t, `{"$sum":[{"$knn":{"query":[1]}},{"$knn":{"query":[1],"default":10}}]}`)
	got = Evaluate(e, [][]Measure{ms(1, 1, 2, 2), ms(2, 3, 3, 4)})
	if len(got) != 2 || got[0].RowID != 2 || got[1].RowID != 1 || got[1].Score != 11 {
		t.Fatalf("default: %+v", got)
	}
}

func TestEvaluateRRF(t *testing.T) {
	rrf := `{"$mul":[{"$val":-1},{"$sum":[
		{"$div":{"left":{"$val":1},"right":{"$sum":[{"$val":60},{"$knn":{"query":[1],"return_rank":true}}]}}},
		{"$div":{"left":{"$val":1},"right":{"$sum":[{"$val":60},{"$knn":{"query":{"indices":[1],"values":[1]},"key":"sp","return_rank":true}}]}}}]}]}`
	e := parseRank(t, rrf)
	if leaves := e.KnnLeaves(); len(leaves) != 2 || leaves[1].Sparse == nil || leaves[1].Key != "sp" {
		t.Fatalf("leaves: %+v", leaves)
	}
	// Record 1 is ranked first by both, so it wins.
	got := Evaluate(e, [][]Measure{ms(1, 0.1, 2, 0.2), ms(1, 0.5, 2, 0.9)})
	if len(got) != 2 || got[0].RowID != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestParsePayloadSelectOrderAndValidation(t *testing.T) {
	p, err := ParsePayload(json.RawMessage(`{"select":{"keys":["n","#score","#document"]},"limit":{"limit":2}}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"#document", "#score", "n"}; len(p.Select) != 3 || p.Select[0] != want[0] || p.Select[2] != want[2] {
		t.Fatalf("select order %v", p.Select)
	}
	if _, err := ParsePayload(json.RawMessage(`{"group_by":{"keys":["g"],"aggregate":{"$min_k":{"keys":["#score"],"k":1}}}}`), 0); err == nil {
		t.Fatal("group_by without rank must fail")
	}
}

func TestGroupRecords(t *testing.T) {
	g := &GroupBy{Keys: []string{"g"}, Aggregate: &Aggregate{Keys: []string{KeyScore}, K: 1}}
	meta := map[int64]wire.Metadata{
		1: {"g": wire.StringValue("x")}, 2: {"g": wire.StringValue("y")}, 3: {"g": wire.StringValue("x")},
	}
	got := GroupRecords(g, ms(1, 0.1, 3, 0.2, 2, 0.3), meta)
	if len(got) != 2 || got[0].RowID != 1 || got[1].RowID != 2 {
		t.Fatalf("got %+v", got)
	}
}
