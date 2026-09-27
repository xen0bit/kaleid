package wire

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"testing"
)

func TestParseValueKinds(t *testing.T) {
	cases := []struct {
		in   string
		kind Kind
		out  string
	}{
		{`true`, KindBool, `true`},
		{`3`, KindInt, `3`},
		{`3.0`, KindFloat, `3.0`},
		{`1e2`, KindFloat, `100.0`},
		{`-2.5`, KindFloat, `-2.5`},
		{`9223372036854775808`, KindFloat, `9.223372036854776e+18`},
		{`"x"`, KindString, `"x"`},
		{`[1,2]`, KindIntArray, `[1,2]`},
		{`[1,2.5]`, KindFloatArray, `[1.0,2.5]`},
		{`["a","b"]`, KindStringArray, `["a","b"]`},
		{`[true,false]`, KindBoolArray, `[true,false]`},
		{`{"indices":[1,3],"values":[0.5,1]}`, KindSparse, `{"#type":"sparse_vector","indices":[1,3],"values":[0.5,1.0]}`},
	}
	for _, c := range cases {
		v, err := ParseValue([]byte(c.in))
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if v.Kind != c.kind {
			t.Errorf("%s: kind %d, want %d", c.in, v.Kind, c.kind)
		}
		if got := string(AppendValue(nil, v)); got != c.out {
			t.Errorf("%s: encoded %s, want %s", c.in, got, c.out)
		}
	}
}

func TestParseValueRejects(t *testing.T) {
	for _, in := range []string{`null`, `{"a":1}`, `[1,"a"]`, `[[1]]`, `[null]`} {
		if _, err := ParseValue([]byte(in)); err == nil {
			t.Errorf("%s: expected error", in)
		}
	}
}

func TestStorageRoundTripKeepsFloatness(t *testing.T) {
	md := Metadata{"i": IntValue(3), "f": FloatValue(3), "big": FloatValue(1e20), "tiny": FloatValue(1.5e-7), "fa": {Kind: KindFloatArray, Floats: []float64{1, 2}}}
	back, err := ParseMetadata(md.StorageJSON())
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range md {
		if back[k].Kind != v.Kind {
			t.Errorf("%s: kind %d, want %d", k, back[k].Kind, v.Kind)
		}
	}
	if back["big"].Float != 1e20 || back["tiny"].Float != 1.5e-7 {
		t.Errorf("float values changed: %v", back)
	}
}

func TestMergeDeletesNullKeys(t *testing.T) {
	x := IntValue(10)
	got := Metadata{"a": IntValue(1), "b": StringValue("keep")}.Merge(UpdateMetadata{"a": &x, "b": nil})
	if len(got) != 1 || got["a"].Int != 10 {
		t.Fatalf("unexpected merge result %v", got)
	}
	if (Metadata{"a": IntValue(1)}).Merge(UpdateMetadata{"a": nil}) != nil {
		t.Fatal("expected nil metadata when all keys are deleted")
	}
}

func TestBase64Embeddings(t *testing.T) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint32(buf, math.Float32bits(0.25))
	binary.LittleEndian.PutUint32(buf[4:], math.Float32bits(0.75))
	raw := `["` + base64.StdEncoding.EncodeToString(buf) + `"]`
	embs, err := ParseEmbeddings([]byte(raw), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(embs) != 1 || embs[0][0] != 0.25 || embs[0][1] != 0.75 {
		t.Fatalf("got %v", embs)
	}
	if _, err := ParseEmbeddings([]byte(`[null]`), false); err == nil {
		t.Fatal("null embedding must be rejected for add")
	}
	if e, err := ParseEmbeddings([]byte(`[null,[1,2]]`), true); err != nil || e[0] != nil {
		t.Fatalf("update embeddings with null: %v %v", e, err)
	}
}

func TestFloatFormatting(t *testing.T) {
	cases := map[float32]string{1: "1.0", 0.49999997: "0.49999997", 0.29289323: "0.29289323", -5: "-5.0"}
	for in, want := range cases {
		if got := FormatFloat32(in); got != want {
			t.Errorf("FormatFloat32(%v) = %s, want %s", in, got, want)
		}
	}
}

func TestSparseValidate(t *testing.T) {
	if err := (&SparseVector{Indices: []uint32{1, 1}, Values: []float32{1, 2}}).Validate(); err == nil {
		t.Error("duplicate indices must fail")
	}
	if err := (&SparseVector{Indices: []uint32{1}, Values: []float32{1, 2}}).Validate(); err == nil {
		t.Error("length mismatch must fail")
	}
}
