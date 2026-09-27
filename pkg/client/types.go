package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/xen0bit/kaleid/internal/wire"
)

// Metadata maps keys to values. Supported value types: bool, string, the Go
// integer types, float32/float64, slices of those, and SparseVector. Values
// decoded from the server are bool, string, int64, float64, []bool,
// []string, []int64, []float64 or *SparseVector.
//
// Integers and floats stay distinct on the wire: float64(3) is sent as 3.0
// and comes back as a float64, int(3) comes back as an int64.
type Metadata map[string]any

// SparseVector is a sparse embedding (sorted indices with values).
type SparseVector struct {
	Indices []uint32
	Values  []float32
	// Tokens optionally labels each index (e.g. BM25 terms).
	Tokens []string
}

// Include selects optional result fields.
type Include string

// Include values.
const (
	IncludeDocuments  Include = "documents"
	IncludeEmbeddings Include = "embeddings"
	IncludeMetadatas  Include = "metadatas"
	IncludeURIs       Include = "uris"
	IncludeDistances  Include = "distances"
)

// Where is a metadata filter, e.g.
//
//	client.Where{"$and": []any{
//		client.Where{"year": client.Where{"$gte": 2020}},
//		client.Where{"tags": client.Where{"$contains": "go"}},
//	}}
type Where = map[string]any

// WhereDocument is a document filter, e.g. {"$contains": "hello"}.
type WhereDocument = map[string]any

func toWireValue(v any) (wire.Value, error) {
	switch x := v.(type) {
	case bool:
		return wire.BoolValue(x), nil
	case string:
		return wire.StringValue(x), nil
	case int:
		return wire.IntValue(int64(x)), nil
	case int8:
		return wire.IntValue(int64(x)), nil
	case int16:
		return wire.IntValue(int64(x)), nil
	case int32:
		return wire.IntValue(int64(x)), nil
	case int64:
		return wire.IntValue(x), nil
	case uint8:
		return wire.IntValue(int64(x)), nil
	case uint16:
		return wire.IntValue(int64(x)), nil
	case uint32:
		return wire.IntValue(int64(x)), nil
	case uint:
		if uint64(x) > math.MaxInt64 {
			return wire.Value{}, fmt.Errorf("integer %d overflows int64", x)
		}
		return wire.IntValue(int64(x)), nil
	case uint64:
		if x > math.MaxInt64 {
			return wire.Value{}, fmt.Errorf("integer %d overflows int64", x)
		}
		return wire.IntValue(int64(x)), nil
	case float32:
		return wire.FloatValue(float64(x)), nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return wire.Value{}, fmt.Errorf("non-finite float %v", x)
		}
		return wire.FloatValue(x), nil
	case json.Number:
		return wire.ParseValue([]byte(x))
	case []bool:
		return wire.Value{Kind: wire.KindBoolArray, Bools: x}, nil
	case []string:
		return wire.Value{Kind: wire.KindStringArray, Strs: x}, nil
	case []int:
		out := make([]int64, len(x))
		for i, n := range x {
			out[i] = int64(n)
		}
		return wire.Value{Kind: wire.KindIntArray, Ints: out}, nil
	case []int64:
		return wire.Value{Kind: wire.KindIntArray, Ints: x}, nil
	case []float64:
		return wire.Value{Kind: wire.KindFloatArray, Floats: x}, nil
	case []float32:
		out := make([]float64, len(x))
		for i, f := range x {
			out[i] = float64(f)
		}
		return wire.Value{Kind: wire.KindFloatArray, Floats: out}, nil
	case SparseVector:
		return wire.SparseValue(&wire.SparseVector{Indices: x.Indices, Values: x.Values, Tokens: x.Tokens}), nil
	case *SparseVector:
		return wire.SparseValue(&wire.SparseVector{Indices: x.Indices, Values: x.Values, Tokens: x.Tokens}), nil
	}
	return wire.Value{}, fmt.Errorf("unsupported metadata value type %T", v)
}

func fromWireValue(v wire.Value) any {
	switch v.Kind {
	case wire.KindBool:
		return v.Bool
	case wire.KindInt:
		return v.Int
	case wire.KindFloat:
		return v.Float
	case wire.KindString:
		return v.Str
	case wire.KindSparse:
		return &SparseVector{Indices: v.Sparse.Indices, Values: v.Sparse.Values, Tokens: v.Sparse.Tokens}
	case wire.KindBoolArray:
		return v.Bools
	case wire.KindIntArray:
		return v.Ints
	case wire.KindFloatArray:
		return v.Floats
	case wire.KindStringArray:
		return v.Strs
	}
	return nil
}

// MarshalJSON encodes metadata keeping the int/float distinction. A nil
// value encodes as null (which deletes the key in Update and Upsert).
func (m Metadata) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	buf := []byte{'{'}
	first := true
	for k, v := range m {
		if !first {
			buf = append(buf, ',')
		}
		first = false
		kb, _ := json.Marshal(k)
		buf = append(buf, kb...)
		buf = append(buf, ':')
		if v == nil {
			buf = append(buf, "null"...)
			continue
		}
		wv, err := toWireValue(v)
		if err != nil {
			return nil, fmt.Errorf("metadata key %q: %w", k, err)
		}
		buf = wire.AppendValue(buf, wv)
	}
	return append(buf, '}'), nil
}

// UnmarshalJSON decodes metadata into typed Go values.
func (m *Metadata) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*m = nil
		return nil
	}
	md, err := wire.ParseMetadata(b)
	if err != nil {
		return err
	}
	out := make(Metadata, len(md))
	for k, v := range md {
		out[k] = fromWireValue(v)
	}
	*m = out
	return nil
}

// base64Embeddings encodes vectors as base64 packed little-endian f32, which
// both Kaleid and Chroma accept for writes. A nil vector encodes as null.
type base64Embeddings [][]float32

func (e base64Embeddings) MarshalJSON() ([]byte, error) {
	buf := []byte{'['}
	for i, v := range e {
		if i > 0 {
			buf = append(buf, ',')
		}
		if v == nil {
			buf = append(buf, "null"...)
			continue
		}
		buf = append(buf, '"')
		buf = append(buf, wire.EncodeBase64Embedding(v)...)
		buf = append(buf, '"')
	}
	return append(buf, ']'), nil
}

// float32s decodes JSON numbers into []float32.
type float32s []float32

func (f *float32s) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*f = nil
		return nil
	}
	v, err := wire.ParseFloatList(b)
	if err != nil {
		return err
	}
	*f = v
	return nil
}

func toFloat32Matrix(in []float32s) [][]float32 {
	if in == nil {
		return nil
	}
	out := make([][]float32, len(in))
	for i, v := range in {
		out[i] = []float32(v)
	}
	return out
}
