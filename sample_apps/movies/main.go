// Movies: search and chat with a movies collection.
//
// A Go port of Chroma's sample_apps/movies (a Next.js app). It serves a web
// page with two tabs:
//
//   - Search: hybrid search (dense embeddings + BM25 keywords, fused with
//     reciprocal rank fusion) over movie overviews.
//   - Chat: an LLM that answers questions by calling a searchMovies tool
//     backed by the same hybrid search.
//
// Usage:
//
//	go run ./sample_apps/movies load              # built-in 60-movie sample
//	go run ./sample_apps/movies load -csv movies_metadata.csv   # The Movies Dataset (Kaggle, CC0)
//	OPENAI_API_KEY=... go run ./sample_apps/movies serve        # http://localhost:3000
//
// Search works without an LLM; chat needs one (see internal/llm for
// LLM_PROVIDER=openai|ollama|gemini|xai).
package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/internal/llm"
	"github.com/xen0bit/kaleid/pkg/client"
	kembed "github.com/xen0bit/kaleid/pkg/embed"
)

//go:embed data/movies.jsonl
var sampleData []byte

//go:embed web
var webFS embed.FS

const (
	collectionName = "movies"
	sparseKey      = "bm25_sparse_vector"
)

// Movie is one dataset record.
type Movie struct {
	Title            string   `json:"title"`
	Year             int      `json:"year"`
	OriginalLanguage string   `json:"original_language"`
	Genres           []string `json:"genres,omitempty"`
	Budget           int64    `json:"budget,omitempty"`
	Overview         string   `json:"overview"`
}

func (m Movie) metadata(bm25 *kembed.BM25) client.Metadata {
	md := client.Metadata{"title": m.Title, "original_language": m.OriginalLanguage, sparseKey: bm25.EncodeDocument(m.Title + " " + m.Overview)}
	if m.Year > 0 {
		md["year"] = m.Year
	}
	if len(m.Genres) > 0 {
		md["genres"] = m.Genres
	}
	if m.Budget > 0 {
		md["budget"] = m.Budget
	}
	return md
}

func readJSONL(r io.Reader) ([]Movie, error) {
	var out []Movie
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var m Movie
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// readMoviesCSV reads movies_metadata.csv from The Movies Dataset.
func readMoviesCSV(path string, limit int) ([]Movie, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.LazyQuotes = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return nil, err
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"title", "overview"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("%s: missing %q column", path, need)
		}
	}
	get := func(rec []string, name string) string {
		if i, ok := col[name]; ok && i < len(rec) {
			return strings.TrimSpace(rec[i])
		}
		return ""
	}
	var out []Movie
	for limit <= 0 || len(out) < limit {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue // skip malformed rows
		}
		m := Movie{Title: get(rec, "title"), Overview: get(rec, "overview"), OriginalLanguage: get(rec, "original_language")}
		if m.Title == "" || m.Overview == "" {
			continue
		}
		if d := get(rec, "release_date"); len(d) >= 4 {
			m.Year, _ = strconv.Atoi(d[:4])
		}
		m.Budget, _ = strconv.ParseInt(get(rec, "budget"), 10, 64)
		out = append(out, m)
	}
	return out, nil
}

func load(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("load", flag.ExitOnError)
	csvPath := flags.String("csv", "", "path to movies_metadata.csv from The Movies Dataset (default: built-in sample)")
	limit := flags.Int("limit", 0, "maximum movies to load from the CSV (0 = all)")
	_ = flags.Parse(args)

	var movies []Movie
	var err error
	if *csvPath != "" {
		movies, err = readMoviesCSV(*csvPath, *limit)
	} else {
		movies, err = readJSONL(strings.NewReader(string(sampleData)))
	}
	exampleenv.Must(err)

	_, err = loadMovies(ctx, exampleenv.Client(), exampleenv.Embedder(), movies, func(done, total int) {
		fmt.Printf("\rloaded %d/%d movies", done, total)
	})
	exampleenv.Must(err)
	fmt.Println()
}

// loadMovies (re)creates the movies collection and loads movies into it.
func loadMovies(ctx context.Context, c *client.Client, emb kembed.Embedder, movies []Movie, progress func(done, total int)) (*client.Collection, error) {
	bm25 := kembed.NewBM25()
	if err := c.DeleteCollection(ctx, collectionName); err != nil && !client.IsNotFound(err) {
		return nil, err
	}
	col, err := c.CreateCollection(ctx, collectionName, &client.CreateCollectionOptions{
		Schema:   kembed.BM25Schema(sparseKey),
		Metadata: client.Metadata{"embedder": emb.Name()},
	})
	if err != nil {
		return nil, err
	}
	const batch = 200
	for i := 0; i < len(movies); i += batch {
		end := min(i+batch, len(movies))
		var ids, docs []string
		var mds []client.Metadata
		for j := i; j < end; j++ {
			ids = append(ids, "movie-"+strconv.Itoa(j))
			docs = append(docs, movies[j].Overview)
			mds = append(mds, movies[j].metadata(bm25))
		}
		vecs, err := emb.Embed(ctx, docs)
		if err != nil {
			return nil, err
		}
		if err := col.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: docs, Metadatas: mds}); err != nil {
			return nil, err
		}
		if progress != nil {
			progress(end, len(movies))
		}
	}
	return col, nil
}

// app holds the server's dependencies.
type app struct {
	col   *client.Collection
	emb   kembed.Embedder
	bm25  *kembed.BM25
	model *llm.Client // nil disables chat
	log   *log.Logger
}

// Result is one movie returned by search.
type Result struct {
	ID       string         `json:"id"`
	Overview string         `json:"overview"`
	Score    float64        `json:"score"`
	Metadata map[string]any `json:"metadata"`
}

// queryMovies runs the hybrid search used by both tabs.
func (a *app) queryMovies(ctx context.Context, query string) ([]Result, error) {
	qv, err := a.emb.Embed(ctx, []string{query})
	if err != nil {
		return nil, err
	}
	const limit = 100
	last := float64(limit)
	rank := client.RRF(60,
		client.Knn(qv[0], client.KnnOptions{Limit: limit, ReturnRank: true, Default: &last}),
		client.KnnSparse(a.bm25.EncodeQuery(query), sparseKey, client.KnnOptions{Limit: limit, ReturnRank: true, Default: &last}),
	)
	start := time.Now()
	res, err := a.col.Search(ctx, client.Search{
		Rank: rank, Limit: 10,
		Select: []string{client.KeyDocument, client.KeyMetadata, client.KeyScore},
	})
	if err != nil {
		return nil, err
	}
	out := make([]Result, len(res.IDs[0]))
	for i, id := range res.IDs[0] {
		md := map[string]any{}
		for k, v := range res.Metadatas[0][i] {
			if k != sparseKey { // the sparse vector is not useful to show
				md[k] = v
			}
		}
		out[i] = Result{ID: id, Overview: *res.Documents[0][i], Score: *res.Scores[0][i], Metadata: md}
	}
	a.log.Printf("search %q: %d results in %s", query, len(out), time.Since(start).Round(time.Millisecond))
	return out, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *app) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing q"})
		return
	}
	results, err := a.queryMovies(r.Context(), q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

const systemPrompt = `You are helping a user chat with a movies dataset.
Use the searchMovies tool to find relevant movies when needed.
<dataset_description>
A dataset of movies. Each record includes the title, an overview of the plot, the original language and the release year, and some records include genres and budget. The collection uses dense embeddings and BM25, enabling hybrid search.
</dataset_description>`

var searchTool = llm.Tool{
	Name:        "searchMovies",
	Description: "Search the movies dataset for relevant films",
	Parameters: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{"type": "string", "description": "The search query to find relevant movies"},
		},
		"required": []string{"query"},
	},
}

type chatRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type chatResponse struct {
	Reply    string   `json:"reply"`
	Searches []string `json:"searches"`
	Results  []Result `json:"results"`
}

// chat runs the tool-calling loop (at most 5 model steps, as in the original).
func (a *app) chat(ctx context.Context, req chatRequest) (chatResponse, error) {
	msgs := []llm.Message{{Role: "system", Content: systemPrompt}}
	for _, m := range req.Messages {
		if m.Role == "user" || m.Role == "assistant" {
			msgs = append(msgs, llm.Message{Role: m.Role, Content: m.Content})
		}
	}
	var resp chatResponse
	for step := 0; step < 5; step++ {
		reply, err := a.model.Chat(ctx, msgs, []llm.Tool{searchTool})
		if err != nil {
			return resp, err
		}
		if len(reply.ToolCalls) == 0 {
			resp.Reply = reply.Content
			return resp, nil
		}
		msgs = append(msgs, reply)
		for _, call := range reply.ToolCalls {
			var args struct {
				Query string `json:"query"`
			}
			result := ""
			if call.Function.Name != searchTool.Name || json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args.Query == "" {
				result = `{"error":"unknown tool or invalid arguments"}`
			} else {
				found, err := a.queryMovies(ctx, args.Query)
				if err != nil {
					return resp, err
				}
				resp.Searches = append(resp.Searches, args.Query)
				resp.Results = append(resp.Results, found...)
				b, _ := json.Marshal(map[string]any{"results": found})
				result = string(b)
			}
			msgs = append(msgs, llm.Message{Role: "tool", ToolCallID: call.ID, Content: result})
		}
	}
	resp.Reply = "I could not finish answering within the allowed number of steps."
	return resp, nil
}

func (a *app) handleChat(w http.ResponseWriter, r *http.Request) {
	if a.model == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "chat needs an LLM: set OPENAI_API_KEY or LLM_PROVIDER (see README)"})
		return
	}
	var req chatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	resp, err := a.chat(r.Context(), req)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("GET /", http.FileServerFS(static))
	mux.HandleFunc("GET /api/search", a.handleSearch)
	mux.HandleFunc("POST /api/chat", a.handleChat)
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"chat": a.model != nil})
	})
	return mux
}

func serve(ctx context.Context, args []string) {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := flags.String("addr", ":3000", "listen address")
	_ = flags.Parse(args)

	logger := log.New(os.Stderr, "movies ", log.LstdFlags)
	c := exampleenv.Client()
	col, err := c.GetCollection(ctx, collectionName)
	if client.IsNotFound(err) {
		exampleenv.Must(fmt.Errorf("collection %q not found; run `go run ./sample_apps/movies load` first", collectionName))
	}
	exampleenv.Must(err)
	a := &app{col: col, emb: exampleenv.Embedder(), bm25: kembed.NewBM25(), log: logger}
	if want, ok := col.Metadata["embedder"].(string); ok && want != a.emb.Name() {
		logger.Printf("warning: collection was loaded with embedder %q but %q is configured", want, a.emb.Name())
	}
	if m, err := llm.FromEnv(); err == nil {
		a.model = m
		logger.Printf("chat enabled: %s %s", m.Provider, m.Model)
	} else {
		logger.Printf("chat disabled: %v", err)
	}
	logger.Printf("listening on http://localhost%s", *addr)
	srv := &http.Server{Addr: *addr, Handler: a.routes(), ReadHeaderTimeout: 10 * time.Second}
	exampleenv.Must(srv.ListenAndServe())
}

func main() {
	usage := "usage: movies load [-csv file] | serve [-addr :3000]"
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "load":
		load(ctx, os.Args[2:])
	case "serve":
		serve(ctx, os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}
