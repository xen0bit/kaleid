// Package search implements Chroma's Search API payload model: filters,
// rank expressions (KNN leaves combined arithmetically, which also expresses
// RRF), group-by aggregation, pagination and projection. Rank evaluation
// mirrors Chroma's worker exactly: every sub-expression evaluates to a domain
// of (record -> score) plus an optional default for records outside it.
package search

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/wire"
)

// Special keys.
const (
	KeyDocument  = "#document"
	KeyEmbedding = "#embedding"
	KeyMetadata  = "#metadata"
	KeyScore     = "#score"
)

// keyRank orders keys like Chroma's derived Ord on its Key enum.
func keyRank(k string) int {
	switch k {
	case KeyDocument:
		return 0
	case KeyEmbedding:
		return 1
	case KeyMetadata:
		return 2
	case KeyScore:
		return 3
	}
	return 4
}

// SortKeys sorts keys in Chroma's Key order (special keys, then fields).
func SortKeys(keys []string) {
	sort.Slice(keys, func(i, j int) bool {
		ri, rj := keyRank(keys[i]), keyRank(keys[j])
		if ri != rj {
			return ri < rj
		}
		return keys[i] < keys[j]
	})
}

// Knn is a KNN leaf.
type Knn struct {
	Dense      []float32
	Sparse     *wire.SparseVector
	Key        string
	Limit      int
	Default    *float32
	ReturnRank bool
}

// Expr is a rank expression node.
type Expr struct {
	Op       string // $abs $div $exp $knn $log $max $min $mul $sub $sum $val
	Children []*Expr
	Knn      *Knn
	Value    float32
}

// Aggregate is a group-by aggregate.
type Aggregate struct {
	Max  bool
	Keys []string
	K    int
}

// GroupBy groups ranked results.
type GroupBy struct {
	Keys      []string
	Aggregate *Aggregate
}

// Active mirrors GroupBy::is_active.
func (g *GroupBy) Active() bool { return g != nil && len(g.Keys) > 0 && g.Aggregate != nil }

// Payload is one search in a batch.
type Payload struct {
	Filter  filter.Expr
	Rank    *Expr
	GroupBy *GroupBy
	Limit   *int
	Offset  int
	Select  []string
}

func unproc(format string, args ...any) error { return apierr.Unprocessable(format, args...) }

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// ParsePayload parses one SearchPayload.
func ParsePayload(raw json.RawMessage, idx int) (Payload, error) {
	var p Payload
	var parts struct {
		Filter  json.RawMessage `json:"filter"`
		Rank    json.RawMessage `json:"rank"`
		GroupBy json.RawMessage `json:"group_by"`
		Limit   json.RawMessage `json:"limit"`
		Select  json.RawMessage `json:"select"`
	}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return p, unproc("searches[%d]: %s", idx, err.Error())
	}
	// filter: the whole object is a where clause; {} or null means none.
	if !isNull(parts.Filter) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(parts.Filter, &obj); err != nil {
			return p, unproc("searches[%d].filter: invalid type, expected a where clause", idx)
		}
		if len(obj) > 0 {
			w, err := filter.Parse(parts.Filter, nil)
			if err != nil {
				return p, unproc("searches[%d].filter: %s", idx, err.Error())
			}
			p.Filter = w
		}
	}
	if !isNull(parts.Rank) {
		e, err := parseExpr(parts.Rank)
		if err != nil {
			return p, unproc("searches[%d].rank: %s", idx, err.Error())
		}
		p.Rank = e
	}
	if !isNull(parts.GroupBy) {
		g, err := parseGroupBy(parts.GroupBy)
		if err != nil {
			return p, unproc("searches[%d].group_by: %s", idx, err.Error())
		}
		p.GroupBy = g
	}
	if !isNull(parts.Limit) {
		var l struct {
			Offset *uint32 `json:"offset"`
			Limit  *uint32 `json:"limit"`
		}
		if err := json.Unmarshal(parts.Limit, &l); err != nil {
			return p, unproc("searches[%d].limit: %s", idx, err.Error())
		}
		if l.Offset != nil {
			p.Offset = int(*l.Offset)
		}
		if l.Limit != nil {
			n := int(*l.Limit)
			p.Limit = &n
		}
	}
	if !isNull(parts.Select) {
		var s struct {
			Keys []string `json:"keys"`
		}
		if err := json.Unmarshal(parts.Select, &s); err != nil {
			return p, unproc("searches[%d].select: %s", idx, err.Error())
		}
		seen := map[string]bool{}
		for _, k := range s.Keys {
			if !seen[k] {
				seen[k] = true
				p.Select = append(p.Select, k)
			}
		}
		SortKeys(p.Select)
	}
	if err := p.validate(); err != nil {
		return p, err
	}
	return p, nil
}

func (p *Payload) validate() error {
	if g := p.GroupBy; g != nil && (len(g.Keys) > 0 || g.Aggregate != nil) {
		if len(g.Keys) == 0 {
			return apierr.Validation("group_by", "group_by keys must not be empty when aggregate is specified")
		}
		if g.Aggregate == nil {
			return apierr.Validation("group_by", "group_by aggregate must be specified when keys are provided")
		}
		for _, k := range g.Keys {
			if k == KeyDocument || k == KeyEmbedding || k == KeyMetadata || k == KeyScore {
				return apierr.Validation("group_by", "group_by keys must be metadata fields, got %s", k)
			}
		}
		if len(g.Aggregate.Keys) == 0 {
			return apierr.Validation("group_by", "aggregate keys must not be empty")
		}
		if g.Aggregate.K == 0 {
			return apierr.Validation("group_by", "aggregate k must be greater than 0")
		}
		if p.Rank == nil {
			return apierr.Validation("group_by", "group_by requires rank expression to be specified")
		}
	}
	var err error
	p.Rank.walkKnn(func(k *Knn) {
		if err == nil && k.Sparse != nil {
			if e := k.Sparse.Validate(); e != nil {
				err = apierr.Validation("rank", "Invalid sparse vector in KNN query: %s", e.Error())
			}
		}
	})
	return err
}

func parseGroupBy(raw json.RawMessage) (*GroupBy, error) {
	var g struct {
		Keys      []string                   `json:"keys"`
		Aggregate map[string]json.RawMessage `json:"aggregate"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	out := &GroupBy{Keys: g.Keys}
	if len(g.Aggregate) > 0 {
		if len(g.Aggregate) != 1 {
			return nil, fmt.Errorf("expected exactly one aggregate operator")
		}
		for op, body := range g.Aggregate {
			if op != "$min_k" && op != "$max_k" {
				return nil, fmt.Errorf("unknown variant `%s`, expected `$min_k` or `$max_k`", op)
			}
			var a struct {
				Keys []string `json:"keys"`
				K    *uint32  `json:"k"`
			}
			if err := json.Unmarshal(body, &a); err != nil {
				return nil, err
			}
			if a.K == nil {
				return nil, fmt.Errorf("missing field `k`")
			}
			out.Aggregate = &Aggregate{Max: op == "$max_k", Keys: a.Keys, K: int(*a.K)}
		}
	}
	return out, nil
}

func parseExpr(raw json.RawMessage) (*Expr, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, fmt.Errorf("invalid rank expression")
	}
	if len(obj) != 1 {
		return nil, fmt.Errorf("rank expression must have exactly one operator")
	}
	for op, body := range obj {
		e := &Expr{Op: op}
		switch op {
		case "$val":
			f, err := parseFloat(body)
			if err != nil {
				return nil, fmt.Errorf("$val: %w", err)
			}
			e.Value = f
		case "$abs", "$exp", "$log":
			c, err := parseExpr(body)
			if err != nil {
				return nil, err
			}
			e.Children = []*Expr{c}
		case "$div", "$sub":
			var lr struct {
				Left  json.RawMessage `json:"left"`
				Right json.RawMessage `json:"right"`
			}
			if err := json.Unmarshal(body, &lr); err != nil || isNull(lr.Left) || isNull(lr.Right) {
				return nil, fmt.Errorf("%s requires left and right", op)
			}
			l, err := parseExpr(lr.Left)
			if err != nil {
				return nil, err
			}
			r, err := parseExpr(lr.Right)
			if err != nil {
				return nil, err
			}
			e.Children = []*Expr{l, r}
		case "$max", "$min", "$mul", "$sum":
			var list []json.RawMessage
			if err := json.Unmarshal(body, &list); err != nil {
				return nil, fmt.Errorf("%s expects a list", op)
			}
			for _, item := range list {
				c, err := parseExpr(item)
				if err != nil {
					return nil, err
				}
				e.Children = append(e.Children, c)
			}
		case "$knn":
			k, err := parseKnn(body)
			if err != nil {
				return nil, err
			}
			e.Knn = k
		default:
			return nil, fmt.Errorf("unknown rank operator `%s`", op)
		}
		return e, nil
	}
	return nil, fmt.Errorf("invalid rank expression")
}

func parseFloat(raw json.RawMessage) (float32, error) {
	var n json.Number
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0, fmt.Errorf("expected a number")
	}
	f, err := strconv.ParseFloat(string(n), 64)
	if err != nil {
		return 0, err
	}
	return float32(f), nil
}

func parseKnn(raw json.RawMessage) (*Knn, error) {
	var k struct {
		Query      json.RawMessage `json:"query"`
		Key        *string         `json:"key"`
		Limit      *uint32         `json:"limit"`
		Default    json.RawMessage `json:"default"`
		ReturnRank bool            `json:"return_rank"`
	}
	if err := json.Unmarshal(raw, &k); err != nil {
		return nil, fmt.Errorf("$knn: %w", err)
	}
	out := &Knn{Key: KeyEmbedding, Limit: 16, ReturnRank: k.ReturnRank}
	if k.Key != nil {
		out.Key = *k.Key
	}
	if k.Limit != nil {
		out.Limit = int(*k.Limit)
	}
	if !isNull(k.Default) {
		f, err := parseFloat(k.Default)
		if err != nil {
			return nil, fmt.Errorf("$knn.default: %w", err)
		}
		out.Default = &f
	}
	q := bytes.TrimSpace(k.Query)
	switch {
	case len(q) > 0 && q[0] == '[':
		v, err := wire.ParseFloatList(q)
		if err != nil {
			return nil, fmt.Errorf("$knn.query: %w", err)
		}
		out.Dense = v
	case len(q) > 0 && q[0] == '{':
		sv, err := wire.ParseSparseVector(q)
		if err != nil {
			return nil, fmt.Errorf("$knn.query: %w", err)
		}
		out.Sparse = sv
	case len(q) > 0 && q[0] == '"':
		return nil, fmt.Errorf("$knn.query: string queries must be embedded by the client before sending")
	default:
		return nil, fmt.Errorf("$knn.query: data did not match any variant of untagged enum QueryVector")
	}
	return out, nil
}

// walkKnn visits KNN leaves in evaluation order (depth-first, left to right).
func (e *Expr) walkKnn(fn func(*Knn)) {
	if e == nil {
		return
	}
	if e.Knn != nil {
		fn(e.Knn)
		return
	}
	for _, c := range e.Children {
		c.walkKnn(fn)
	}
}

// KnnLeaves returns the KNN leaves in evaluation order.
func (e *Expr) KnnLeaves() []*Knn {
	var out []*Knn
	e.walkKnn(func(k *Knn) { out = append(out, k) })
	return out
}

// Measure is a (record, score) pair.
type Measure struct {
	RowID int64
	Score float32
}

type domain struct {
	support map[int64]float32
	def     *float32
}

func flat(v float32) domain { return domain{support: map[int64]float32{}, def: &v} }

func (d domain) mapf(op func(float32) float32) domain {
	out := domain{support: make(map[int64]float32, len(d.support))}
	for k, v := range d.support {
		out.support[k] = op(v)
	}
	if d.def != nil {
		v := op(*d.def)
		out.def = &v
	}
	return out
}

func merge(l, r domain, op func(a, b float32) float32) domain {
	out := domain{support: map[int64]float32{}}
	switch {
	case l.def != nil && r.def != nil:
		for k, lv := range l.support {
			rv, ok := r.support[k]
			if !ok {
				rv = *r.def
			}
			out.support[k] = op(lv, rv)
		}
		for k, rv := range r.support {
			if _, ok := l.support[k]; !ok {
				out.support[k] = op(*l.def, rv)
			}
		}
		v := op(*l.def, *r.def)
		out.def = &v
	case l.def != nil:
		for k, rv := range r.support {
			lv, ok := l.support[k]
			if !ok {
				lv = *l.def
			}
			out.support[k] = op(lv, rv)
		}
	case r.def != nil:
		for k, lv := range l.support {
			rv, ok := r.support[k]
			if !ok {
				rv = *r.def
			}
			out.support[k] = op(lv, rv)
		}
	default:
		for k, lv := range l.support {
			if rv, ok := r.support[k]; ok {
				out.support[k] = op(lv, rv)
			}
		}
	}
	return out
}

type evaluator struct {
	results [][]Measure
	next    int
}

func (ev *evaluator) eval(e *Expr) domain {
	switch e.Op {
	case "$abs":
		return ev.eval(e.Children[0]).mapf(func(v float32) float32 { return float32(math.Abs(float64(v))) })
	case "$exp":
		return ev.eval(e.Children[0]).mapf(func(v float32) float32 { return float32(math.Exp(float64(v))) })
	case "$log":
		return ev.eval(e.Children[0]).mapf(func(v float32) float32 { return float32(math.Log(float64(v))) })
	case "$div":
		return merge(ev.eval(e.Children[0]), ev.eval(e.Children[1]), func(a, b float32) float32 { return a / b })
	case "$sub":
		return merge(ev.eval(e.Children[0]), ev.eval(e.Children[1]), func(a, b float32) float32 { return a - b })
	case "$sum":
		acc := flat(0)
		for _, c := range e.Children {
			acc = merge(acc, ev.eval(c), func(a, b float32) float32 { return a + b })
		}
		return acc
	case "$mul":
		acc := flat(1)
		for _, c := range e.Children {
			acc = merge(acc, ev.eval(c), func(a, b float32) float32 { return a * b })
		}
		return acc
	case "$max":
		acc := flat(-math.MaxFloat32)
		for _, c := range e.Children {
			acc = merge(acc, ev.eval(c), func(a, b float32) float32 { return max(a, b) })
		}
		return acc
	case "$min":
		acc := flat(math.MaxFloat32)
		for _, c := range e.Children {
			acc = merge(acc, ev.eval(c), func(a, b float32) float32 { return min(a, b) })
		}
		return acc
	case "$val":
		return flat(e.Value)
	case "$knn":
		var res []Measure
		if ev.next < len(ev.results) {
			res = ev.results[ev.next]
		}
		ev.next++
		d := domain{support: make(map[int64]float32, len(res)), def: e.Knn.Default}
		for i, m := range res {
			if e.Knn.ReturnRank {
				d.support[m.RowID] = float32(i)
			} else {
				d.support[m.RowID] = m.Score
			}
		}
		return d
	}
	return domain{support: map[int64]float32{}}
}

// Evaluate ranks records given the KNN results (one list per leaf, in
// KnnLeaves order, each sorted best-first). Output is sorted ascending by
// score, ties broken by record order.
func Evaluate(e *Expr, knnResults [][]Measure) []Measure {
	ev := &evaluator{results: knnResults}
	d := ev.eval(e)
	out := make([]Measure, 0, len(d.support))
	for k, v := range d.support {
		out = append(out, Measure{RowID: k, Score: v})
	}
	SortMeasures(out)
	return out
}

// SortMeasures sorts ascending by score then row id (NaN sorts last).
func SortMeasures(ms []Measure) {
	sort.Slice(ms, func(i, j int) bool {
		a, b := ms[i].Score, ms[j].Score
		an, bn := math.IsNaN(float64(a)), math.IsNaN(float64(b))
		if an != bn {
			return !an
		}
		if a != b && !an {
			return a < b
		}
		return ms[i].RowID < ms[j].RowID
	})
}

// GroupRecords applies a ranked group-by. meta provides each record's
// metadata. The result is re-sorted by score.
func GroupRecords(g *GroupBy, ranked []Measure, meta map[int64]wire.Metadata) []Measure {
	if !g.Active() || len(ranked) == 0 {
		return ranked
	}
	type rec struct {
		m     Measure
		group []keyVal
	}
	extract := func(m Measure, keys []string) []keyVal {
		out := make([]keyVal, len(keys))
		for i, k := range keys {
			if k == KeyScore {
				out[i] = keyVal{ok: true, v: wire.FloatValue(float64(m.Score))}
				continue
			}
			if v, ok := meta[m.RowID][k]; ok {
				out[i] = keyVal{ok: true, v: v}
			}
		}
		return out
	}
	groups := map[string][]rec{}
	var order []string
	for _, m := range ranked {
		gk := extract(m, g.Keys)
		sig := signature(gk)
		if _, ok := groups[sig]; !ok {
			order = append(order, sig)
		}
		groups[sig] = append(groups[sig], rec{m: m})
	}
	var out []Measure
	for _, sig := range order {
		members := groups[sig]
		sort.SliceStable(members, func(i, j int) bool {
			a := extract(members[i].m, g.Aggregate.Keys)
			b := extract(members[j].m, g.Aggregate.Keys)
			c := compareKeyVals(a, b)
			if g.Aggregate.Max {
				return c > 0
			}
			return c < 0
		})
		for i := 0; i < len(members) && i < g.Aggregate.K; i++ {
			out = append(out, members[i].m)
		}
	}
	SortMeasures(out)
	return out
}

type keyVal struct {
	ok bool
	v  wire.Value
}

func signature(kvs []keyVal) string {
	var b []byte
	for _, kv := range kvs {
		if !kv.ok {
			b = append(b, "\x00none"...)
		} else {
			b = append(b, byte(kv.v.Kind))
			b = wire.AppendValue(b, kv.v)
		}
		b = append(b, 0x1f)
	}
	return string(b)
}

// compareKeyVals orders tuples like Rust's Option<MetadataValue> ordering:
// None sorts first; numbers compare numerically.
func compareKeyVals(a, b []keyVal) int {
	for i := range a {
		x, y := a[i], b[i]
		switch {
		case !x.ok && !y.ok:
			continue
		case !x.ok:
			return -1
		case !y.ok:
			return 1
		}
		if c := compareValues(x.v, y.v); c != 0 {
			return c
		}
	}
	return 0
}

func compareValues(x, y wire.Value) int {
	if x.IsNumber() && y.IsNumber() {
		a, b := x.AsFloat(), y.AsFloat()
		switch {
		case a < b:
			return -1
		case a > b:
			return 1
		}
		return 0
	}
	if x.Kind != y.Kind {
		if x.Kind < y.Kind {
			return -1
		}
		return 1
	}
	switch x.Kind {
	case wire.KindBool:
		switch {
		case x.Bool == y.Bool:
			return 0
		case !x.Bool:
			return -1
		}
		return 1
	case wire.KindString:
		switch {
		case x.Str < y.Str:
			return -1
		case x.Str > y.Str:
			return 1
		}
	}
	return 0
}
