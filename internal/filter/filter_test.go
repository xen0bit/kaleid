package filter

import (
	"regexp"
	"strings"
	"testing"
)

func TestParseWhereValid(t *testing.T) {
	valid := []string{
		`{"a":1}`, `{"a":"x"}`, `{"a":true}`, `{"a":1.5}`,
		`{"a":{"$eq":1}}`, `{"a":{"$ne":"x"}}`, `{"a":{"$gt":1}}`, `{"a":{"$lte":2.5}}`,
		`{"a":{"$in":[1,2]}}`, `{"a":{"$nin":["x","y"]}}`, `{"a":{"$in":[1.5,2]}}`,
		`{"a":{"$contains":"x"}}`, `{"a":{"$not_contains":3}}`,
		`{"#document":{"$contains":"x"}}`, `{"#document":{"$regex":"^a.*"}}`,
		`{"$and":[{"a":1},{"b":2}]}`, `{"$or":[]}`,
	}
	for _, w := range valid {
		if _, err := Parse([]byte(w), nil); err != nil {
			t.Errorf("%s: unexpected error %v", w, err)
		}
	}
}

func TestParseWhereInvalid(t *testing.T) {
	invalid := []string{
		`{}`, `{"a":1,"b":2}`, `{"a":{"$bogus":1}}`, `{"a":{"$gt":"x"}}`,
		`{"a":{"$in":[]}}`, `{"a":{"$in":["x",1]}}`, `{"a":{"$in":[1,2.5]}}`,
		`{"a":{"$regex":"x"}}`, `{"#document":{"$contains":1}}`, `{"$gt":1}`,
		`{"a":null}`, `{"a":[1]}`, `{"a":{"$eq":1,"$ne":2}}`,
	}
	for _, w := range invalid {
		if _, err := Parse([]byte(w), nil); err == nil {
			t.Errorf("%s: expected error", w)
		} else if !strings.Contains(err.Error(), "Invalid where clause") && !strings.Contains(err.Error(), "Regex") {
			t.Errorf("%s: unexpected message %q", w, err.Error())
		}
	}
}

func TestParseWhereDocument(t *testing.T) {
	if _, err := Parse(nil, []byte(`{"$and":[{"$contains":"a"},{"$not_regex":"b+"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{`{}`, `{"$contains":1}`, `{"$eq":"x"}`, `{"$contains":"a","$regex":"b"}`} {
		if _, err := Parse(nil, []byte(w)); err == nil {
			t.Errorf("%s: expected error", w)
		}
	}
}

func TestCompileNotSemantics(t *testing.T) {
	e, err := Parse([]byte(`{"a":{"$ne":1}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	args := &Args{}
	sql, err := Compile(e, DefaultColumns, args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sql, "(NOT ") {
		t.Fatalf("$ne must negate containment so missing keys match: %s", sql)
	}
	if args.Vals[0] != `{"a":1}` {
		t.Fatalf("unexpected param %v", args.Vals)
	}
}

func TestLikePatternEscapes(t *testing.T) {
	if got := LikePattern(`50%_off\`); got != `%50\%\_off\\%` {
		t.Fatalf("got %q", got)
	}
}

// TestTranslateRegexAgreesWithRE2 checks the translated pattern on a small
// corpus, using Go's regexp (with PostgreSQL-only escapes mapped back) as the
// reference. The PostgreSQL-side behaviour is covered by integration tests.
func TestTranslateRegex(t *testing.T) {
	cases := map[string]string{
		`^F.o`:     `^F[^\n]o`,
		`\bAPI\b`:  `\yAPI\y`,
		`a|bc`:     `(?:a|bc)`,
		`(?i)ab`:   `[Aa][Bb]`,
		`\d+`:      `(?:[0-9])+`,
		`x{2,3}?`:  `(?:x){2,3}?`,
		`\.`:       `\.`,
		`(?s)a.b`:  `a.b`,
		`[^a]`:     "[\\u0001-`b-\\ud7ff\\ue000-\\U0010ffff]",
		`colou?r$`: `colo(?:u)?r$`,
	}
	for in, want := range cases {
		got, err := TranslateRegex(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Errorf("TranslateRegex(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := TranslateRegex(`(`); err == nil {
		t.Error("invalid regex must fail")
	}
	if _, err := TranslateRegex(`a{300}`); err == nil {
		t.Error("repetition above 255 must fail")
	}
	_ = regexp.MustCompile
}
