package filter

import (
	"fmt"
	"strings"

	"github.com/xen0bit/kaleid/internal/wire"
)

// Args accumulates positional SQL parameters.
type Args struct{ Vals []any }

// Add appends a parameter and returns its placeholder.
func (a *Args) Add(v any) string {
	a.Vals = append(a.Vals, v)
	return fmt.Sprintf("$%d", len(a.Vals))
}

// Columns names the columns the compiler targets (allowing table aliases).
type Columns struct {
	ID, Document, Metadata string
}

// DefaultColumns targets an unaliased record table.
var DefaultColumns = Columns{ID: "id", Document: "document", Metadata: "metadata"}

// Compile renders e as a boolean SQL expression.
//
// Semantics follow local Chroma: metadata is never NULL in storage (an empty
// object stands for "no metadata"), $ne / $nin / $not_contains match records
// that lack the key, numbers compare across int and float, and bool never
// equals a number.
func Compile(e Expr, cols Columns, args *Args) (string, error) {
	switch x := e.(type) {
	case nil:
		return "TRUE", nil
	case Composite:
		if len(x.Children) == 0 {
			if x.Or {
				return "FALSE", nil
			}
			return "TRUE", nil
		}
		parts := make([]string, len(x.Children))
		for i, c := range x.Children {
			s, err := Compile(c, cols, args)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		sep := " AND "
		if x.Or {
			sep = " OR "
		}
		return "(" + strings.Join(parts, sep) + ")", nil
	case Cmp:
		return compileCmp(x, cols, args), nil
	case Set:
		return compileSet(x, cols, args), nil
	case ArrayContains:
		p := args.Add(containsJSON(x.Key, x.Val))
		expr := fmt.Sprintf("(%s @> %s::jsonb)", cols.Metadata, p)
		if x.Not {
			return "(NOT " + expr + ")", nil
		}
		return expr, nil
	case Doc:
		var cond string
		if x.Regex {
			pat, err := TranslateRegex(x.Pattern)
			if err != nil {
				return "", err
			}
			cond = fmt.Sprintf("%s ~ %s", cols.Document, args.Add(pat))
		} else {
			cond = fmt.Sprintf("%s LIKE %s", cols.Document, args.Add(LikePattern(x.Pattern)))
		}
		if x.Not {
			return fmt.Sprintf("(NOT COALESCE(%s, false))", cond), nil
		}
		return "(" + cond + ")", nil
	}
	return "", fmt.Errorf("unknown filter expression %T", e)
}

// LikePattern builds an escaped %substring% LIKE pattern.
func LikePattern(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

func objectJSON(key string, val []byte) string {
	var b []byte
	b = append(b, '{')
	kb := wire.AppendValue(nil, wire.StringValue(key))
	b = append(b, kb...)
	b = append(b, ':')
	b = append(b, val...)
	b = append(b, '}')
	return string(b)
}

func containsJSON(key string, v wire.Value) string {
	return objectJSON(key, append(append([]byte{'['}, wire.StorageValueJSON(v)...), ']'))
}

func idColumnCmp(x Cmp, cols Columns, args *Args) (string, bool) {
	if x.Key != "#id" || x.Val.Kind != wire.KindString {
		return "", false
	}
	switch x.Op {
	case "$eq":
		return fmt.Sprintf("(%s = %s)", cols.ID, args.Add(x.Val.Str)), true
	case "$ne":
		return fmt.Sprintf("(%s <> %s)", cols.ID, args.Add(x.Val.Str)), true
	}
	return "", false
}

func compileCmp(x Cmp, cols Columns, args *Args) string {
	if s, ok := idColumnCmp(x, cols, args); ok {
		return s
	}
	switch x.Op {
	case "$eq", "$ne":
		p := args.Add(objectJSON(x.Key, wire.StorageValueJSON(x.Val)))
		expr := fmt.Sprintf("(%s @> %s::jsonb)", cols.Metadata, p)
		if x.Op == "$ne" {
			return "(NOT " + expr + ")"
		}
		return expr
	}
	// Range operators are numeric only (the parser guarantees a number).
	sqlOp := map[string]string{"$gt": ">", "$gte": ">=", "$lt": "<", "$lte": "<="}[x.Op]
	k := args.Add(x.Key)
	v := args.Add(numericLiteral(x.Val))
	return fmt.Sprintf("(CASE WHEN jsonb_typeof(%[1]s -> %[2]s::text) = 'number' THEN (%[1]s ->> %[2]s::text)::numeric %[3]s %[4]s::numeric ELSE false END)",
		cols.Metadata, k, sqlOp, v)
}

func numericLiteral(v wire.Value) string {
	if v.Kind == wire.KindInt {
		return fmt.Sprintf("%d", v.Int)
	}
	return string(wire.StorageValueJSON(v))
}

func compileSet(x Set, cols Columns, args *Args) string {
	if x.Key == "#id" && len(x.Vals) > 0 && x.Vals[0].Kind == wire.KindString {
		ids := make([]string, len(x.Vals))
		for i, v := range x.Vals {
			ids[i] = v.Str
		}
		expr := fmt.Sprintf("(%s = ANY(%s::text[]))", cols.ID, args.Add(ids))
		if x.Not {
			return "(NOT " + expr + ")"
		}
		return expr
	}
	vals := make([]string, len(x.Vals))
	for i, v := range x.Vals {
		vals[i] = string(wire.StorageValueJSON(v))
	}
	expr := fmt.Sprintf("((%s -> %s::text) = ANY(%s::jsonb[]))", cols.Metadata, args.Add(x.Key), args.Add(vals))
	if x.Not {
		return fmt.Sprintf("(NOT COALESCE(%s, false))", expr)
	}
	return expr
}
