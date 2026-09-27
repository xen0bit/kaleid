// Package filter parses Chroma where / where_document clauses into an AST
// (mirroring chroma_types::where_parsing exactly) and compiles them to SQL
// against a per-collection record table.
package filter

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/wire"
)

// Expr is a filter expression node.
type Expr interface{ isExpr() }

// Composite is $and / $or.
type Composite struct {
	Or       bool
	Children []Expr
}

// Cmp is a primitive comparison: $eq $ne $gt $gte $lt $lte.
type Cmp struct {
	Key string
	Op  string
	Val wire.Value
}

// Set is $in / $nin over a homogeneous list.
type Set struct {
	Key  string
	Not  bool
	Vals []wire.Value
}

// ArrayContains is $contains / $not_contains on a metadata array key.
type ArrayContains struct {
	Key string
	Not bool
	Val wire.Value
}

// Doc is a document $contains / $not_contains / $regex / $not_regex.
type Doc struct {
	Not     bool
	Regex   bool
	Pattern string
}

func (Composite) isExpr()     {}
func (Cmp) isExpr()           {}
func (Set) isExpr()           {}
func (ArrayContains) isExpr() {}
func (Doc) isExpr()           {}

var (
	errWhere    = apierr.InvalidArgument("Invalid where clause")
	errWhereDoc = apierr.InvalidArgument("Invalid where document clause")
)

// jnum classifies a JSON number like serde_json::Value.
type jnum struct {
	isI64, isF64 bool
	i            int64
	f            float64
}

func classifyNumber(n json.Number) (jnum, bool) {
	s := string(n)
	if strings.ContainsAny(s, ".eE") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return jnum{}, false
		}
		return jnum{isF64: true, f: f}, true
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return jnum{isI64: true, i: i, f: float64(i)}, true
	}
	// u64 beyond i64: neither is_i64 nor is_f64, but as_f64 works.
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return jnum{}, false
	}
	return jnum{f: f}, true
}

// decodeAny decodes JSON with numbers kept as json.Number.
func decodeAny(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

func isNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

// Parse parses the where and where_document fields and ANDs them together.
// Returns nil when both are absent.
func Parse(where, whereDoc json.RawMessage) (Expr, error) {
	var w, wd Expr
	if !isNull(where) {
		v, err := decodeAny(where)
		if err != nil {
			return nil, errWhere
		}
		if w, err = ParseWhere(v); err != nil {
			return nil, err
		}
	}
	if !isNull(whereDoc) {
		v, err := decodeAny(whereDoc)
		if err != nil {
			return nil, errWhereDoc
		}
		if wd, err = ParseWhereDocument(v); err != nil {
			return nil, err
		}
	}
	switch {
	case w != nil && wd != nil:
		return Composite{Children: []Expr{w, wd}}, nil
	case w != nil:
		return w, nil
	}
	return wd, nil
}

// ParseWhereDocument mirrors parse_where_document.
func ParseWhereDocument(v any) (Expr, error) {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 {
		return nil, errWhereDoc
	}
	for key, val := range obj {
		if key == "$and" || key == "$or" {
			arr, ok := val.([]any)
			if !ok {
				return nil, errWhereDoc
			}
			c := Composite{Or: key == "$or", Children: make([]Expr, 0, len(arr))}
			for _, child := range arr {
				e, err := ParseWhereDocument(child)
				if err != nil {
					return nil, err
				}
				c.Children = append(c.Children, e)
			}
			return c, nil
		}
		s, ok := val.(string)
		if !ok {
			return nil, errWhereDoc
		}
		switch key {
		case "$contains":
			return Doc{Pattern: s}, nil
		case "$not_contains":
			return Doc{Not: true, Pattern: s}, nil
		case "$regex", "$not_regex":
			if err := ValidateRegex(s); err != nil {
				return nil, err
			}
			return Doc{Regex: true, Not: key == "$not_regex", Pattern: s}, nil
		}
		return nil, errWhereDoc
	}
	return nil, errWhereDoc
}

// ParseWhere mirrors parse_where.
func ParseWhere(v any) (Expr, error) {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 {
		return nil, errWhere
	}
	for key, val := range obj {
		if key == "$and" || key == "$or" {
			arr, ok := val.([]any)
			if !ok {
				return nil, errWhere
			}
			c := Composite{Or: key == "$or", Children: make([]Expr, 0, len(arr))}
			for _, child := range arr {
				e, err := ParseWhere(child)
				if err != nil {
					return nil, err
				}
				c.Children = append(c.Children, e)
			}
			return c, nil
		}
		if strings.HasPrefix(key, "$") {
			return nil, errWhere
		}
		return parseField(key, val)
	}
	return nil, errWhere
}

func parseField(key string, val any) (Expr, error) {
	switch x := val.(type) {
	case string:
		return Cmp{Key: key, Op: "$eq", Val: wire.StringValue(x)}, nil
	case bool:
		return Cmp{Key: key, Op: "$eq", Val: wire.BoolValue(x)}, nil
	case json.Number:
		n, ok := classifyNumber(x)
		switch {
		case ok && n.isF64:
			return Cmp{Key: key, Op: "$eq", Val: wire.FloatValue(n.f)}, nil
		case ok && n.isI64:
			return Cmp{Key: key, Op: "$eq", Val: wire.IntValue(n.i)}, nil
		}
		return nil, errWhere
	case map[string]any:
		if len(x) != 1 {
			return nil, errWhere
		}
		for op, operand := range x {
			return parseOperator(key, op, operand)
		}
	}
	return nil, errWhere
}

func containsOp(op string) (bool, bool) {
	switch op {
	case "$contains":
		return true, false
	case "$not_contains":
		return true, true
	}
	return false, false
}

func parseOperator(key, op string, operand any) (Expr, error) {
	switch x := operand.(type) {
	case []any:
		if op != "$in" && op != "$nin" {
			return nil, errWhere
		}
		not := op == "$nin"
		if len(x) == 0 {
			return nil, errWhere
		}
		vals := make([]wire.Value, len(x))
		switch first := x[0].(type) {
		case string:
			for i, e := range x {
				s, ok := e.(string)
				if !ok {
					return nil, errWhere
				}
				vals[i] = wire.StringValue(s)
			}
		case bool:
			for i, e := range x {
				b, ok := e.(bool)
				if !ok {
					return nil, errWhere
				}
				vals[i] = wire.BoolValue(b)
			}
		case json.Number:
			n0, ok := classifyNumber(first)
			if !ok {
				return nil, errWhere
			}
			switch {
			case n0.isF64:
				for i, e := range x {
					num, ok := e.(json.Number)
					if !ok {
						return nil, errWhere
					}
					n, ok := classifyNumber(num)
					if !ok {
						return nil, errWhere
					}
					vals[i] = wire.FloatValue(n.f)
				}
			case n0.isI64:
				for i, e := range x {
					num, ok := e.(json.Number)
					if !ok {
						return nil, errWhere
					}
					n, ok := classifyNumber(num)
					if !ok || !n.isI64 {
						return nil, errWhere
					}
					vals[i] = wire.IntValue(n.i)
				}
			default:
				return nil, errWhere
			}
		default:
			return nil, errWhere
		}
		return Set{Key: key, Not: not, Vals: vals}, nil
	case string:
		if isC, not := containsOp(op); isC {
			if key == "#document" {
				return Doc{Not: not, Pattern: x}, nil
			}
			return ArrayContains{Key: key, Not: not, Val: wire.StringValue(x)}, nil
		}
		if op == "$regex" || op == "$not_regex" {
			if key != "#document" {
				return nil, errWhere
			}
			if err := ValidateRegex(x); err != nil {
				return nil, err
			}
			return Doc{Regex: true, Not: op == "$not_regex", Pattern: x}, nil
		}
		if op == "$eq" || op == "$ne" {
			return Cmp{Key: key, Op: op, Val: wire.StringValue(x)}, nil
		}
		return nil, errWhere
	case bool:
		if isC, not := containsOp(op); isC {
			if key == "#document" {
				return nil, errWhere
			}
			return ArrayContains{Key: key, Not: not, Val: wire.BoolValue(x)}, nil
		}
		if op == "$eq" || op == "$ne" {
			return Cmp{Key: key, Op: op, Val: wire.BoolValue(x)}, nil
		}
		return nil, errWhere
	case json.Number:
		n, ok := classifyNumber(x)
		if !ok || (!n.isF64 && !n.isI64) {
			return nil, errWhere
		}
		v := wire.IntValue(n.i)
		if n.isF64 {
			v = wire.FloatValue(n.f)
		}
		if isC, not := containsOp(op); isC {
			if key == "#document" {
				return nil, errWhere
			}
			return ArrayContains{Key: key, Not: not, Val: v}, nil
		}
		switch op {
		case "$eq", "$ne", "$lt", "$lte", "$gt", "$gte":
			return Cmp{Key: key, Op: op, Val: v}, nil
		}
		return nil, errWhere
	}
	return nil, errWhere
}
