package api

import (
	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/wire"
)

// checkFilterIndexes mirrors Schema::is_metadata_where_indexing_enabled:
// filtering on a key/type whose inverted index is disabled, or on document
// content when FTS is disabled, is an InvalidArgument error.
func checkFilterIndexes(s *collection.Schema, e filter.Expr) error {
	switch x := e.(type) {
	case nil:
		return nil
	case filter.Composite:
		for _, c := range x.Children {
			if err := checkFilterIndexes(s, c); err != nil {
				return err
			}
		}
		return nil
	case filter.Doc:
		if !ftsEnabled(s) {
			return apierr.InvalidArgument("Cannot filter using full-text search because FTS indexing is disabled")
		}
		return nil
	case filter.Cmp:
		return checkKey(s, x.Key, x.Val.Kind)
	case filter.ArrayContains:
		return checkKey(s, x.Key, x.Val.Kind)
	case filter.Set:
		if len(x.Vals) > 0 {
			return checkKey(s, x.Key, x.Vals[0].Kind)
		}
	}
	return nil
}

func ftsEnabled(s *collection.Schema) bool {
	if vt := s.Keys[collection.DocumentKey]; vt != nil && vt.String != nil && vt.String.FtsIndex != nil {
		return vt.String.FtsIndex.Enabled
	}
	if s.Defaults.String != nil && s.Defaults.String.FtsIndex != nil {
		return s.Defaults.String.FtsIndex.Enabled
	}
	return true
}

func inverted(vt *collection.ValueTypes, kind wire.Kind) (*collection.SimpleIndexType, bool) {
	if vt == nil {
		return nil, false
	}
	switch kind {
	case wire.KindBool:
		if vt.Bool != nil {
			return vt.Bool.BoolInvertedIndex, true
		}
	case wire.KindInt:
		if vt.Int != nil {
			return vt.Int.IntInvertedIndex, true
		}
	case wire.KindFloat:
		if vt.Float != nil {
			return vt.Float.FloatInvertedIndex, true
		}
	case wire.KindString:
		if vt.String != nil {
			return vt.String.StringInvertedIndex, true
		}
	}
	return nil, false
}

var kindNames = map[wire.Kind]string{wire.KindBool: "Bool", wire.KindInt: "Int", wire.KindFloat: "Float", wire.KindString: "Str"}

func checkKey(s *collection.Schema, key string, kind wire.Kind) error {
	name, ok := kindNames[kind]
	if !ok {
		return nil
	}
	idx, found := inverted(s.Keys[key], kind)
	if !found {
		idx, found = inverted(&s.Defaults, kind)
	}
	if !found || idx == nil {
		return nil
	}
	if !idx.Enabled {
		return apierr.InvalidArgument("Cannot filter using metadata key '%s' with type '%s' because indexing is disabled", key, name)
	}
	return nil
}
