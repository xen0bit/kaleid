package wire

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"strconv"
)

// ParseEmbeddings decodes an EmbeddingsPayload: either a list of float lists
// or a list of base64 strings holding packed little-endian f32 values. When
// allowNull is set (UpdateEmbeddingsPayload), individual entries may be null.
func ParseEmbeddings(raw json.RawMessage, allowNull bool) ([][]float32, error) {
	raw = bytes.TrimSpace(raw)
	bad := decodeErr("data did not match any variant of untagged enum EmbeddingsPayload")
	if len(raw) == 0 || raw[0] != '[' {
		return nil, bad
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, bad
	}
	out := make([][]float32, len(elems))
	for i, e := range elems {
		e = bytes.TrimSpace(e)
		switch {
		case len(e) > 0 && e[0] == 'n':
			if !allowNull {
				return nil, bad
			}
			out[i] = nil
		case len(e) > 0 && e[0] == '"':
			var s string
			if err := json.Unmarshal(e, &s); err != nil {
				return nil, bad
			}
			v, err := DecodeBase64Embedding(s)
			if err != nil {
				return nil, bad
			}
			out[i] = v
		case len(e) > 0 && e[0] == '[':
			v, err := parseFloatList(e)
			if err != nil {
				return nil, bad
			}
			out[i] = v
		default:
			return nil, bad
		}
	}
	return out, nil
}

// ParseFloatList parses a JSON array of numbers into f32.
func ParseFloatList(raw json.RawMessage) ([]float32, error) { return parseFloatList(raw) }

func parseFloatList(raw []byte) ([]float32, error) {
	var nums []json.Number
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&nums); err != nil {
		return nil, err
	}
	out := make([]float32, len(nums))
	for i, n := range nums {
		f, err := strconv.ParseFloat(string(n), 64)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

// DecodeBase64Embedding unpacks base64 little-endian f32 values.
func DecodeBase64Embedding(s string) ([]float32, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		b, err = base64.RawStdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}
	}
	if len(b)%4 != 0 {
		return nil, decodeErr("base64 embedding length is not a multiple of 4")
	}
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out, nil
}

// AppendFloat32s appends a JSON array of f32 values (null when v is nil).
func AppendFloat32s(buf []byte, v []float32) []byte {
	if v == nil {
		return append(buf, "null"...)
	}
	buf = append(buf, '[')
	for i, x := range v {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, FormatFloat32(x)...)
	}
	return append(buf, ']')
}

// Float32s is a []float32 that marshals with f32 shortest formatting.
type Float32s []float32

// MarshalJSON implements json.Marshaler.
func (f Float32s) MarshalJSON() ([]byte, error) { return AppendFloat32s(nil, f), nil }
