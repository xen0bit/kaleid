// Package wire implements the JSON encodings used by the Chroma HTTP API:
// metadata values, embeddings (float arrays or base64 packed f32), sparse
// vectors, and float formatting that preserves the int/float distinction.
package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Kind identifies the type of a metadata value.
type Kind uint8

const (
	KindBool Kind = iota + 1
	KindInt
	KindFloat
	KindString
	KindSparse
	KindBoolArray
	KindIntArray
	KindFloatArray
	KindStringArray
)

// Value is a single metadata value. Exactly one of the typed fields is
// meaningful, selected by Kind.
type Value struct {
	Kind   Kind
	Bool   bool
	Int    int64
	Float  float64
	Str    string
	Sparse *SparseVector
	Bools  []bool
	Ints   []int64
	Floats []float64
	Strs   []string
}

// Metadata is a record or collection metadata map.
type Metadata map[string]Value

// UpdateMetadata is a metadata map where a nil value deletes the key.
type UpdateMetadata map[string]*Value

func BoolValue(b bool) Value            { return Value{Kind: KindBool, Bool: b} }
func IntValue(i int64) Value            { return Value{Kind: KindInt, Int: i} }
func FloatValue(f float64) Value        { return Value{Kind: KindFloat, Float: f} }
func StringValue(s string) Value        { return Value{Kind: KindString, Str: s} }
func SparseValue(s *SparseVector) Value { return Value{Kind: KindSparse, Sparse: s} }

// IsNumber reports whether the value is a scalar int or float.
func (v Value) IsNumber() bool { return v.Kind == KindInt || v.Kind == KindFloat }

// AsFloat returns the numeric value as float64 (valid for int and float kinds).
func (v Value) AsFloat() float64 {
	if v.Kind == KindInt {
		return float64(v.Int)
	}
	return v.Float
}

// IsArray reports whether the value is one of the array kinds.
func (v Value) IsArray() bool {
	switch v.Kind {
	case KindBoolArray, KindIntArray, KindFloatArray, KindStringArray:
		return true
	}
	return false
}

// TypeName returns the Chroma schema value-type bucket for the value
// ("bool", "int", "float", "string", "sparse_vector"). Arrays map to the
// bucket of their element type, as in Chroma's ensure_key_from_metadata.
func (v Value) TypeName() string {
	switch v.Kind {
	case KindBool, KindBoolArray:
		return "bool"
	case KindInt, KindIntArray:
		return "int"
	case KindFloat, KindFloatArray:
		return "float"
	case KindString, KindStringArray:
		return "string"
	case KindSparse:
		return "sparse_vector"
	}
	return ""
}

// SparseVector is Chroma's sparse vector representation.
type SparseVector struct {
	Indices []uint32
	Values  []float32
	Tokens  []string
}

var (
	ErrSparseLengthMismatch = errors.New("Sparse vector indices, values, and tokens (when present) must have the same length")
	ErrSparseNotSorted      = errors.New("Sparse vector indices must be sorted in strictly ascending order (no duplicates)")
)

// Validate mirrors chroma_types SparseVector::validate.
func (s *SparseVector) Validate() error {
	if len(s.Indices) != len(s.Values) {
		return ErrSparseLengthMismatch
	}
	if s.Tokens != nil && len(s.Tokens) != len(s.Indices) {
		return ErrSparseLengthMismatch
	}
	for i := 1; i < len(s.Indices); i++ {
		if s.Indices[i] <= s.Indices[i-1] {
			return ErrSparseNotSorted
		}
	}
	return nil
}

// Normalized returns a copy with indices sorted ascending (values/tokens
// permuted accordingly). Used for query vectors which are not required to be
// pre-sorted by every client.
func (s *SparseVector) Normalized() *SparseVector {
	n := len(s.Indices)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return s.Indices[idx[a]] < s.Indices[idx[b]] })
	out := &SparseVector{Indices: make([]uint32, n), Values: make([]float32, n)}
	if s.Tokens != nil {
		out.Tokens = make([]string, n)
	}
	for i, j := range idx {
		out.Indices[i] = s.Indices[j]
		out.Values[i] = s.Values[j]
		if s.Tokens != nil {
			out.Tokens[i] = s.Tokens[j]
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

// DecodeError describes a JSON shape error (Chroma returns these as 422s).
type DecodeError struct{ Msg string }

func (e *DecodeError) Error() string { return e.Msg }

func decodeErr(format string, args ...any) error {
	return &DecodeError{Msg: fmt.Sprintf(format, args...)}
}

// ParseValue parses a single (non-null) metadata value.
func ParseValue(raw json.RawMessage) (Value, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return Value{}, decodeErr("empty value")
	}
	switch raw[0] {
	case 't', 'f':
		var b bool
		if err := json.Unmarshal(raw, &b); err != nil {
			return Value{}, decodeErr("invalid boolean")
		}
		return BoolValue(b), nil
	case '"':
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return Value{}, decodeErr("invalid string")
		}
		return StringValue(s), nil
	case '{':
		sv, err := ParseSparseVector(raw)
		if err != nil {
			return Value{}, decodeErr("data did not match any variant of untagged enum MetadataValue")
		}
		return SparseValue(sv), nil
	case '[':
		return parseArray(raw)
	case 'n':
		return Value{}, decodeErr("data did not match any variant of untagged enum MetadataValue")
	default:
		return parseNumber(raw)
	}
}

func parseNumber(raw []byte) (Value, error) {
	s := string(raw)
	isFloat := bytes.ContainsAny(raw, ".eE")
	if !isFloat {
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return IntValue(i), nil
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return Value{}, decodeErr("data did not match any variant of untagged enum MetadataValue")
	}
	return FloatValue(f), nil
}

func parseArray(raw []byte) (Value, error) {
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return Value{}, decodeErr("data did not match any variant of untagged enum MetadataValue")
	}
	bad := decodeErr("data did not match any variant of untagged enum MetadataValue")
	if len(elems) == 0 {
		// serde's untagged enum matches the first array variant (bool array).
		return Value{Kind: KindBoolArray, Bools: []bool{}}, nil
	}
	vals := make([]Value, len(elems))
	for i, e := range elems {
		e = bytes.TrimSpace(e)
		if len(e) == 0 || e[0] == '[' || e[0] == '{' || e[0] == 'n' {
			return Value{}, bad
		}
		v, err := ParseValue(e)
		if err != nil {
			return Value{}, bad
		}
		vals[i] = v
	}
	// Determine homogeneous element type. Ints coerce into a float array
	// when mixed with floats (serde tries IntArray, then FloatArray).
	allBool, allInt, allNum, allStr := true, true, true, true
	for _, v := range vals {
		allBool = allBool && v.Kind == KindBool
		allInt = allInt && v.Kind == KindInt
		allNum = allNum && v.IsNumber()
		allStr = allStr && v.Kind == KindString
	}
	switch {
	case allBool:
		out := make([]bool, len(vals))
		for i, v := range vals {
			out[i] = v.Bool
		}
		return Value{Kind: KindBoolArray, Bools: out}, nil
	case allInt:
		out := make([]int64, len(vals))
		for i, v := range vals {
			out[i] = v.Int
		}
		return Value{Kind: KindIntArray, Ints: out}, nil
	case allNum:
		out := make([]float64, len(vals))
		for i, v := range vals {
			out[i] = v.AsFloat()
		}
		return Value{Kind: KindFloatArray, Floats: out}, nil
	case allStr:
		out := make([]string, len(vals))
		for i, v := range vals {
			out[i] = v.Str
		}
		return Value{Kind: KindStringArray, Strs: out}, nil
	}
	return Value{}, bad
}

// ParseSparseVector parses {"indices":[...],"values":[...]} with an optional
// "#type":"sparse_vector" tag and optional "tokens".
func ParseSparseVector(raw []byte) (*SparseVector, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	for k := range m {
		switch k {
		case "#type", "indices", "values", "tokens":
		default:
			return nil, fmt.Errorf("unknown field %q", k)
		}
	}
	if t, ok := m["#type"]; ok {
		var s string
		if err := json.Unmarshal(t, &s); err != nil || s != "sparse_vector" {
			return nil, errors.New("invalid #type")
		}
	}
	ir, ok1 := m["indices"]
	vr, ok2 := m["values"]
	if !ok1 || !ok2 {
		return nil, errors.New("missing indices or values")
	}
	var idx []int64
	if err := json.Unmarshal(ir, &idx); err != nil {
		return nil, err
	}
	var vals []float64
	if err := json.Unmarshal(vr, &vals); err != nil {
		return nil, err
	}
	sv := &SparseVector{Indices: make([]uint32, len(idx)), Values: make([]float32, len(vals))}
	for i, x := range idx {
		if x < 0 || x > math.MaxUint32 {
			return nil, errors.New("index out of range")
		}
		sv.Indices[i] = uint32(x)
	}
	for i, x := range vals {
		sv.Values[i] = float32(x)
	}
	if tr, ok := m["tokens"]; ok && !bytes.Equal(bytes.TrimSpace(tr), []byte("null")) {
		if err := json.Unmarshal(tr, &sv.Tokens); err != nil {
			return nil, err
		}
	}
	return sv, nil
}

// ParseMetadata parses a metadata object. A JSON null input yields nil.
func ParseMetadata(raw json.RawMessage) (Metadata, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, decodeErr("invalid type: expected a map")
	}
	md := make(Metadata, len(obj))
	for k, v := range obj {
		val, err := ParseValue(v)
		if err != nil {
			return nil, decodeErr("%s: %s", k, err.Error())
		}
		md[k] = val
	}
	return md, nil
}

// ParseUpdateMetadata parses a metadata object where null deletes a key.
func ParseUpdateMetadata(raw json.RawMessage) (UpdateMetadata, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, decodeErr("invalid type: expected a map")
	}
	md := make(UpdateMetadata, len(obj))
	for k, v := range obj {
		if bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			md[k] = nil
			continue
		}
		val, err := ParseValue(v)
		if err != nil {
			return nil, decodeErr("%s: %s", k, err.Error())
		}
		md[k] = &val
	}
	return md, nil
}

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

// FormatFloat64 formats a float the way serde_json/ryu does closely enough
// that every client parses it back as a float: integral values keep ".0".
func FormatFloat64(f float64) string {
	s := strconv.FormatFloat(f, 'g', -1, 64)
	return ensureFloatMarker(s)
}

// FormatFloat32 formats an f32 with shortest round-trip precision.
func FormatFloat32(f float32) string {
	s := strconv.FormatFloat(float64(f), 'g', -1, 32)
	return ensureFloatMarker(s)
}

func ensureFloatMarker(s string) string {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '.', 'e', 'E', 'n', 'N', 'I':
			return s
		}
	}
	return s + ".0"
}

// storageFloat formats a float for JSONB storage. JSONB numerics preserve the
// literal's scale, so a trailing ".0" survives a round trip, which is how we
// keep int and float distinct. Exponent notation would be normalized away by
// Postgres, so fixed notation is used.
func storageFloat(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !bytes.ContainsRune([]byte(s), '.') {
		s += ".0"
	}
	return s
}

func appendString(buf []byte, s string) []byte {
	b, _ := json.Marshal(s)
	return append(buf, b...)
}

// AppendValue appends the API JSON encoding of v.
func AppendValue(buf []byte, v Value) []byte { return appendValue(buf, v, false) }

func appendValue(buf []byte, v Value, storage bool) []byte {
	ff := FormatFloat64
	if storage {
		ff = storageFloat
	}
	switch v.Kind {
	case KindBool:
		return strconv.AppendBool(buf, v.Bool)
	case KindInt:
		return strconv.AppendInt(buf, v.Int, 10)
	case KindFloat:
		return append(buf, ff(v.Float)...)
	case KindString:
		return appendString(buf, v.Str)
	case KindSparse:
		return AppendSparse(buf, v.Sparse)
	case KindBoolArray:
		buf = append(buf, '[')
		for i, x := range v.Bools {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = strconv.AppendBool(buf, x)
		}
		return append(buf, ']')
	case KindIntArray:
		buf = append(buf, '[')
		for i, x := range v.Ints {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = strconv.AppendInt(buf, x, 10)
		}
		return append(buf, ']')
	case KindFloatArray:
		buf = append(buf, '[')
		for i, x := range v.Floats {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, ff(x)...)
		}
		return append(buf, ']')
	case KindStringArray:
		buf = append(buf, '[')
		for i, x := range v.Strs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendString(buf, x)
		}
		return append(buf, ']')
	}
	return append(buf, "null"...)
}

// AppendSparse appends the tagged sparse vector encoding.
func AppendSparse(buf []byte, s *SparseVector) []byte {
	buf = append(buf, `{"#type":"sparse_vector","indices":[`...)
	for i, x := range s.Indices {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = strconv.AppendUint(buf, uint64(x), 10)
	}
	buf = append(buf, `],"values":[`...)
	for i, x := range s.Values {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, FormatFloat32(x)...)
	}
	buf = append(buf, ']')
	if s.Tokens != nil {
		buf = append(buf, `,"tokens":`...)
		b, _ := json.Marshal(s.Tokens)
		buf = append(buf, b...)
	}
	return append(buf, '}')
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AppendMetadata appends the API JSON encoding of md (null if nil).
func AppendMetadata(buf []byte, md Metadata) []byte {
	if md == nil {
		return append(buf, "null"...)
	}
	buf = append(buf, '{')
	for i, k := range sortedKeys(md) {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendString(buf, k)
		buf = append(buf, ':')
		buf = appendValue(buf, md[k], false)
	}
	return append(buf, '}')
}

// MarshalJSON implements json.Marshaler.
func (md Metadata) MarshalJSON() ([]byte, error) { return AppendMetadata(nil, md), nil }

// MarshalJSON implements json.Marshaler.
func (v Value) MarshalJSON() ([]byte, error) { return AppendValue(nil, v), nil }

// StorageJSON encodes metadata for JSONB storage (nil -> nil).
func (md Metadata) StorageJSON() []byte {
	if md == nil {
		return nil
	}
	buf := []byte{'{'}
	for i, k := range sortedKeys(md) {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendString(buf, k)
		buf = append(buf, ':')
		buf = appendValue(buf, md[k], true)
	}
	return append(buf, '}')
}

// StorageValueJSON encodes a single value for JSONB comparison/storage.
func StorageValueJSON(v Value) []byte { return appendValue(nil, v, true) }

// Clone returns a shallow copy of md.
func (md Metadata) Clone() Metadata {
	if md == nil {
		return nil
	}
	out := make(Metadata, len(md))
	for k, v := range md {
		out[k] = v
	}
	return out
}

// ToMetadata converts update metadata (dropping deletions) to plain metadata.
func (u UpdateMetadata) ToMetadata() Metadata {
	if u == nil {
		return nil
	}
	out := make(Metadata, len(u))
	for k, v := range u {
		if v != nil {
			out[k] = *v
		}
	}
	return out
}

// Merge applies an update to md, returning the result. Null values delete.
// An empty result becomes nil (Chroma returns null metadata when no keys remain).
func (md Metadata) Merge(u UpdateMetadata) Metadata {
	out := md.Clone()
	if out == nil {
		out = Metadata{}
	}
	for k, v := range u {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = *v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
