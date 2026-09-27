// Package compat is a differential test harness: every scenario is replayed
// against a reference Chroma server and a Kaleid server in lockstep, and the
// normalized responses (status code + JSON body) must match.
//
// Run with:
//
//	CHROMA_URL=http://localhost:8000 KALEID_URL=http://localhost:8001 go test ./test/compat
package compat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

type server struct {
	name string
	base string
	vars map[string]string
}

type step struct {
	method, path, body string
	// capture stores response["id"] under this variable name.
	capture string
	// ignore lists top-level JSON keys whose values are not compared.
	ignore []string
	// unordered compares top-level arrays (and the ids/documents/metadatas
	// columns of get responses) as multisets.
	unordered bool
}

var uuidRE = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

func (s *server) do(t *testing.T, st step) (int, any) {
	t.Helper()
	path := st.path
	body := st.body
	for k, v := range s.vars {
		path = strings.ReplaceAll(path, "{"+k+"}", v)
		body = strings.ReplaceAll(body, "{"+k+"}", v)
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(st.method, s.base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s %s: %v", s.name, st.method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var v any
	if len(bytes.TrimSpace(raw)) > 0 {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s %s %s: invalid JSON %q", s.name, st.method, path, raw)
		}
	}
	if st.capture != "" {
		if m, ok := v.(map[string]any); ok {
			if id, ok := m["id"].(string); ok {
				s.vars[st.capture] = id
			}
		}
	}
	return resp.StatusCode, v
}

// normalize masks UUIDs and machine-dependent values.
func normalize(v any, ignore map[string]bool, depth int) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			if depth == 0 && ignore[k] {
				continue
			}
			if k == "num_threads" {
				continue
			}
			out[k] = normalize(val, ignore, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = normalize(e, ignore, depth+1)
		}
		return out
	case string:
		return uuidRE.ReplaceAllString(x, "<uuid>")
	}
	return v
}

func sortAny(v any) any {
	arr, ok := v.([]any)
	if !ok {
		return v
	}
	cp := append([]any{}, arr...)
	sort.Slice(cp, func(i, j int) bool {
		a, _ := json.Marshal(cp[i])
		b, _ := json.Marshal(cp[j])
		return string(a) < string(b)
	})
	return cp
}

func equal(a, b any, path string) error {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: type mismatch %T vs %T", path, a, b)
		}
		keys := map[string]bool{}
		for k := range x {
			keys[k] = true
		}
		for k := range y {
			keys[k] = true
		}
		for k := range keys {
			if err := equal(x[k], y[k], path+"."+k); err != nil {
				return err
			}
		}
		return nil
	case []any:
		y, ok := b.([]any)
		if !ok {
			return fmt.Errorf("%s: type mismatch %T vs %T", path, a, b)
		}
		if len(x) != len(y) {
			return fmt.Errorf("%s: length %d vs %d", path, len(x), len(y))
		}
		for i := range x {
			if err := equal(x[i], y[i], fmt.Sprintf("%s[%d]", path, i)); err != nil {
				return err
			}
		}
		return nil
	case json.Number:
		y, ok := b.(json.Number)
		if !ok {
			return fmt.Errorf("%s: %v vs %v", path, a, b)
		}
		fa, _ := x.Float64()
		fb, _ := y.Float64()
		if math.Abs(fa-fb) > 1e-5*math.Max(1, math.Abs(fa)) {
			return fmt.Errorf("%s: %s vs %s", path, x, y)
		}
		// int vs float distinction matters to Python clients.
		if strings.ContainsAny(string(x), ".eE") != strings.ContainsAny(string(y), ".eE") {
			return fmt.Errorf("%s: numeric type differs: %s vs %s", path, x, y)
		}
		return nil
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		return fmt.Errorf("%s: %v vs %v", path, a, b)
	}
	return nil
}

func servers(t *testing.T) (*server, *server) {
	ref, kal := os.Getenv("CHROMA_URL"), os.Getenv("KALEID_URL")
	if ref == "" || kal == "" {
		t.Skip("CHROMA_URL and KALEID_URL must be set")
	}
	return &server{name: "chroma", base: ref, vars: map[string]string{}},
		&server{name: "kaleid", base: kal, vars: map[string]string{}}
}

func run(t *testing.T, steps []step) {
	ref, kal := servers(t)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	for i, st := range steps {
		st.path = strings.ReplaceAll(st.path, "{sfx}", suffix)
		st.body = strings.ReplaceAll(st.body, "{sfx}", suffix)
		c1, v1 := ref.do(t, st)
		c2, v2 := kal.do(t, st)
		label := fmt.Sprintf("step %d: %s %s %s", i, st.method, st.path, trunc(st.body))
		if c1 != c2 {
			t.Errorf("%s\n  status chroma=%d kaleid=%d\n  chroma: %s\n  kaleid: %s", label, c1, c2, dump(v1), dump(v2))
			continue
		}
		ign := map[string]bool{}
		for _, k := range st.ignore {
			ign[k] = true
		}
		n1, n2 := normalize(v1, ign, 0), normalize(v2, ign, 0)
		if st.unordered {
			n1, n2 = unorder(n1), unorder(n2)
		}
		if err := equal(n1, n2, "$"); err != nil {
			t.Errorf("%s\n  %v\n  chroma: %s\n  kaleid: %s", label, err, dump(v1), dump(v2))
		}
	}
}

func unorder(v any) any {
	if m, ok := v.(map[string]any); ok {
		out := map[string]any{}
		for k, val := range m {
			out[k] = sortAny(val)
		}
		return out
	}
	return sortAny(v)
}

func trunc(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func dump(v any) string {
	b, _ := json.Marshal(v)
	return trunc(string(b))
}

const coll = "/api/v2/tenants/default_tenant/databases/default_database/collections"
