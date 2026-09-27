package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xen0bit/kaleid/internal/llm"
	"github.com/xen0bit/kaleid/pkg/client"
	kembed "github.com/xen0bit/kaleid/pkg/embed"
)

func TestReadMoviesCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "movies_metadata.csv")
	csv := "adult,budget,id,original_language,overview,release_date,title\n" +
		"False,30000000,862,en,\"Led by Woody, Andy's toys live happily, until Buzz arrives.\",1995-10-30,Toy Story\n" +
		"False,0,1,en,,1990-01-01,No Overview\n"
	if err := os.WriteFile(path, []byte(csv), 0o600); err != nil {
		t.Fatal(err)
	}
	movies, err := readMoviesCSV(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 1 || movies[0].Title != "Toy Story" || movies[0].Year != 1995 || movies[0].Budget != 30000000 {
		t.Fatalf("got %+v", movies)
	}
}

// TestSearchAndChat loads the built-in sample into a live server (KALEID_URL)
// and drives both endpoints, with a fake LLM that calls the search tool once.
func TestSearchAndChat(t *testing.T) {
	base := os.Getenv("KALEID_URL")
	if base == "" {
		t.Skip("KALEID_URL not set")
	}
	ctx := context.Background()
	movies, err := readJSONL(strings.NewReader(string(sampleData)))
	if err != nil {
		t.Fatal(err)
	}
	emb := kembed.NewHashing(384)
	col, err := loadMovies(ctx, client.New(base), emb, movies, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, _ := col.Count(ctx); n != len(movies) {
		t.Fatalf("loaded %d of %d movies", n, len(movies))
	}

	calls := 0
	fakeLLM := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var req struct {
			Messages []llm.Message `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		last := req.Messages[len(req.Messages)-1]
		if last.Role == "tool" {
			if !strings.Contains(last.Content, "Spirited Away") {
				t.Errorf("tool result missing expected movie: %.200s", last.Content)
			}
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"Try Spirited Away."}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"t1","type":"function","function":{"name":"searchMovies","arguments":"{\"query\":\"girl works in a bathhouse for a witch\"}"}}]}}]}`))
	}))
	defer fakeLLM.Close()

	a := &app{col: col, emb: emb, bm25: kembed.NewBM25(), log: log.New(io.Discard, "", 0),
		model: &llm.Client{Provider: "fake", BaseURL: fakeLLM.URL, Model: "fake"}}
	srv := httptest.NewServer(a.routes())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/search?q=" + "space+station+computer+turns+against+crew")
	if err != nil {
		t.Fatal(err)
	}
	var sr struct {
		Results []Result `json:"results"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&sr)
	resp.Body.Close()
	if len(sr.Results) == 0 || sr.Results[0].Metadata["title"] != "2001: A Space Odyssey" {
		t.Fatalf("unexpected search results: %+v", sr.Results)
	}
	if _, leaked := sr.Results[0].Metadata[sparseKey]; leaked {
		t.Fatal("sparse vector must be stripped from results")
	}

	body := strings.NewReader(`{"messages":[{"role":"user","content":"Recommend an animated fantasy film"}]}`)
	resp, err = http.Post(srv.URL+"/api/chat", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	var cr chatResponse
	_ = json.NewDecoder(resp.Body).Decode(&cr)
	resp.Body.Close()
	if cr.Reply != "Try Spirited Away." || len(cr.Searches) != 1 || calls != 2 {
		t.Fatalf("chat: %+v (llm calls %d)", cr, calls)
	}

	page, err := http.Get(srv.URL + "/")
	if err != nil || page.StatusCode != 200 {
		t.Fatalf("index page: %v %v", page.StatusCode, err)
	}
	page.Body.Close()
}
