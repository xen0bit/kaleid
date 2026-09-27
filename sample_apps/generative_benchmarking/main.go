// Generative benchmarking: build a retrieval benchmark from your own
// documents and use it to compare embedding models.
//
// A Go port of Chroma's sample_apps/generative_benchmarking (see Chroma's
// technical report at https://research.trychroma.com/generative-benchmarking).
// The pipeline:
//
//  1. filter    an LLM keeps documents that are relevant to your use case and
//     complete enough to answer questions
//  2. generate  an LLM writes one realistic user query per kept document
//  3. evaluate  documents are loaded into Kaleid with an embedder, each query
//     retrieves the top 10, and NDCG / MAP / Recall / Precision at 1, 3, 5 and
//     10 are computed (the query's own document is the relevant one)
//  4. compare   print results files side by side
//
// Example, using Chroma's documentation as the corpus:
//
//	cd sample_apps/generative_benchmarking
//	OPENAI_API_KEY=... go run . filter
//	OPENAI_API_KEY=... go run . generate
//	EMBEDDER=openai OPENAI_API_KEY=... EMBEDDING_MODEL=text-embedding-3-small go run . evaluate
//	EMBEDDER=hash go run . evaluate
//	go run . compare results/*.json
//
// `generate -heuristic` builds keyword queries without an LLM; use it only to
// smoke-test the pipeline, since it is not a meaningful benchmark.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/internal/llm"
	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

const (
	defaultContext  = "This is a technical support bot for Chroma, a vector database company often used by developers for building AI applications."
	defaultExamples = `how to add to a collection
filter by metadata
retrieve embeddings when querying
how to use openai embedding function when adding to collection`
)

// Query is one generated benchmark query; ID is the relevant document.
type Query struct {
	ID    string `json:"id"`
	Query string `json:"query"`
}

// Result is a saved evaluation.
type Result struct {
	Model   string  `json:"model"`
	Queries int     `json:"queries"`
	Results Metrics `json:"results"`
}

func readJSON(path string, v any) {
	b, err := os.ReadFile(path)
	exampleenv.Must(err)
	exampleenv.Must(json.Unmarshal(b, v))
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", "  ")
	exampleenv.Must(err)
	exampleenv.Must(os.MkdirAll(filepath.Dir(path), 0o755))
	exampleenv.Must(os.WriteFile(path, append(b, '\n'), 0o644))
	fmt.Println("wrote", path)
}

func sortedIDs(corpus map[string]string) []string {
	ids := make([]string, 0, len(corpus))
	for id := range corpus {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func filterCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("filter", flag.ExitOnError)
	corpusPath := fs.String("corpus", "data/chroma_docs.json", "corpus JSON: {id: text}")
	out := fs.String("out", "data/filtered_ids.json", "output: JSON list of kept ids")
	useCase := fs.String("context", defaultContext, "description of your use case")
	_ = fs.Parse(args)

	var corpus map[string]string
	readJSON(*corpusPath, &corpus)
	model, err := llm.FromEnv()
	exampleenv.Must(err)

	const system = `You are an assistant specialized in filtering documents based on specific criteria.

Given a document and a criterion, evaluate whether the document meets the criterion and output a single word: "yes" if the document meets the criterion, or "no" if it does not. Do not include any extra text or formatting, simply "yes" or "no".`
	criteria := []string{
		"The document is relevant to the following context: " + *useCase,
		"The document is complete, meaning that it contains useful information to answer queries and does not only serve as an introduction to the main content that users may be looking for.",
	}
	var kept []string
	ids := sortedIDs(corpus)
	for i, id := range ids {
		pass := true
		for _, criterion := range criteria {
			prompt := fmt.Sprintf("Evaluate the following document with the criterion below.\n\nCriterion: %s\n\nDocument: %s\n\n"+
				`Output a single word: "yes" if the document meets the criterion, or "no" if it does not. Do not include any extra text or formatting, simply "yes" or "no".`,
				criterion, corpus[id])
			answer, err := model.Complete(ctx, system, prompt)
			exampleenv.Must(err)
			if strings.ToLower(strings.Trim(answer, ` ."'`)) != "yes" {
				pass = false
				break
			}
		}
		if pass {
			kept = append(kept, id)
		}
		fmt.Printf("\rfiltered %d/%d documents, kept %d", i+1, len(ids), len(kept))
	}
	fmt.Println()
	writeJSON(*out, kept)
}

// heuristicQuery builds a keyword query from a document's most frequent
// distinctive words. It needs no LLM but is only a smoke test.
func heuristicQuery(text string) string {
	counts := map[string]int{}
	var order []string
	for _, w := range embed.Tokenize(text) {
		if len(w) < 4 || embed.EnglishStopwords[w] {
			continue
		}
		if counts[w] == 0 {
			order = append(order, w)
		}
		counts[w]++
	}
	sort.SliceStable(order, func(i, j int) bool { return counts[order[i]] > counts[order[j]] })
	if len(order) > 5 {
		order = order[:5]
	}
	return strings.Join(order, " ")
}

func generateCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	corpusPath := fs.String("corpus", "data/chroma_docs.json", "corpus JSON: {id: text}")
	idsPath := fs.String("ids", "data/filtered_ids.json", "ids to generate queries for (from filter); all documents if the file does not exist")
	out := fs.String("out", "data/queries.json", "output: generated queries")
	useCase := fs.String("context", defaultContext, "description of your use case")
	examples := fs.String("examples", defaultExamples, "example user queries, one per line")
	heuristic := fs.Bool("heuristic", false, "build keyword queries without an LLM (smoke test only)")
	_ = fs.Parse(args)

	var corpus map[string]string
	readJSON(*corpusPath, &corpus)
	ids := sortedIDs(corpus)
	if _, err := os.Stat(*idsPath); err == nil {
		readJSON(*idsPath, &ids)
	}

	var model *llm.Client
	if !*heuristic {
		var err error
		model, err = llm.FromEnv()
		exampleenv.Must(err)
	}
	const system = "You are an assistant specialized in generating queries to curate a high-quality synthetic dataset.\n\nSimply output the query without any additional words or formatting."
	var queries []Query
	for i, id := range ids {
		doc, ok := corpus[id]
		if !ok {
			continue
		}
		var q string
		if *heuristic {
			q = heuristicQuery(doc)
		} else {
			prompt := fmt.Sprintf(`Consider the context:
%s

Based on the following piece of text:
<text>
%s
<text>

Please generate a realistic query that a user may ask relevant to the information provided above.

Here are some example queries that users have asked which you should consider when generating your query:
<example-queries>
%s
<example-queries>

Do not repeat the example queries, they are only provided to give you an idea of the type of queries that users ask.
Make your query relevant to the information provided above and keep it in a similar style to the example queries, which may not always be in a complete question format.

Simply output the query without any additional words.`, *useCase, doc, *examples)
			var err error
			q, err = model.Complete(ctx, system, prompt)
			exampleenv.Must(err)
		}
		if q != "" {
			queries = append(queries, Query{ID: id, Query: q})
		}
		fmt.Printf("\rgenerated %d/%d queries", i+1, len(ids))
	}
	fmt.Println()
	writeJSON(*out, queries)
}

// runBenchmark loads the corpus with emb and scores the queries.
func runBenchmark(ctx context.Context, c *client.Client, emb embed.Embedder, corpus map[string]string, queries []Query, collectionName string) (Metrics, error) {
	if err := c.DeleteCollection(ctx, collectionName); err != nil && !client.IsNotFound(err) {
		return Metrics{}, err
	}
	col, err := c.CreateCollection(ctx, collectionName, &client.CreateCollectionOptions{
		HNSW: &client.HNSWConfig{Space: client.SpaceCosine},
	})
	if err != nil {
		return Metrics{}, err
	}
	ids := sortedIDs(corpus)
	const batch = 100
	for i := 0; i < len(ids); i += batch {
		end := min(i+batch, len(ids))
		docs := make([]string, 0, end-i)
		for _, id := range ids[i:end] {
			docs = append(docs, corpus[id])
		}
		vecs, err := emb.Embed(ctx, docs)
		if err != nil {
			return Metrics{}, err
		}
		if err := col.Add(ctx, client.Records{IDs: ids[i:end], Embeddings: vecs, Documents: docs}); err != nil {
			return Metrics{}, err
		}
	}

	ranked := map[string][]string{}
	relevant := map[string]map[string]bool{}
	for i := 0; i < len(queries); i += batch {
		end := min(i+batch, len(queries))
		texts := make([]string, 0, end-i)
		for _, q := range queries[i:end] {
			texts = append(texts, q.Query)
		}
		vecs, err := emb.Embed(ctx, texts)
		if err != nil {
			return Metrics{}, err
		}
		res, err := col.Query(ctx, client.QueryOptions{Embeddings: vecs, NResults: 10, Include: []client.Include{client.IncludeDistances}})
		if err != nil {
			return Metrics{}, err
		}
		for j, q := range queries[i:end] {
			// Each query is its own id: several queries may target one document.
			qid := fmt.Sprintf("q%d", i+j)
			ranked[qid] = res.IDs[j]
			relevant[qid] = map[string]bool{q.ID: true}
		}
	}
	return Evaluate(ranked, relevant, []int{1, 3, 5, 10}), nil
}

func evaluateCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("evaluate", flag.ExitOnError)
	corpusPath := fs.String("corpus", "data/chroma_docs.json", "corpus JSON: {id: text}")
	queriesPath := fs.String("queries", "data/queries.json", "queries from generate")
	resultsDir := fs.String("results", "results", "directory for results files")
	_ = fs.Parse(args)

	var corpus map[string]string
	readJSON(*corpusPath, &corpus)
	var queries []Query
	readJSON(*queriesPath, &queries)
	emb := exampleenv.Embedder()
	c := exampleenv.Client()

	name := "gb-" + strings.NewReplacer("/", "-", ":", "-", ".", "-", "_", "-").Replace(emb.Name())
	metrics, err := runBenchmark(ctx, c, emb, corpus, queries, name)
	exampleenv.Must(err)
	res := Result{Model: emb.Name(), Queries: len(queries), Results: metrics}
	printTable([]string{emb.Name()}, []Result{res})
	writeJSON(filepath.Join(*resultsDir, time.Now().Format("2006-01-02--15-04-05")+".json"), res)
}

func printTable(labels []string, results []Result) {
	rows := []struct {
		group string
		get   func(Result) map[string]float64
		keys  []string
	}{
		{"NDCG", func(r Result) map[string]float64 { return r.Results.NDCG }, []string{"NDCG@1", "NDCG@3", "NDCG@5", "NDCG@10"}},
		{"MAP", func(r Result) map[string]float64 { return r.Results.MAP }, []string{"MAP@1", "MAP@3", "MAP@5", "MAP@10"}},
		{"Recall", func(r Result) map[string]float64 { return r.Results.Recall }, []string{"Recall@1", "Recall@3", "Recall@5", "Recall@10"}},
		{"Precision", func(r Result) map[string]float64 { return r.Results.Precision }, []string{"P@1", "P@3", "P@5", "P@10"}},
	}
	fmt.Printf("%-12s", "metric")
	for _, l := range labels {
		fmt.Printf(" %22s", l)
	}
	fmt.Println()
	for _, row := range rows {
		for _, k := range row.keys {
			fmt.Printf("%-12s", k)
			for _, r := range results {
				fmt.Printf(" %22.5f", row.get(r)[k])
			}
			fmt.Println()
		}
	}
}

func compareCmd(args []string) {
	if len(args) == 0 {
		exampleenv.Must(fmt.Errorf("usage: compare results/*.json"))
	}
	var labels []string
	var results []Result
	for _, path := range args {
		var r Result
		readJSON(path, &r)
		labels = append(labels, r.Model)
		results = append(results, r)
	}
	printTable(labels, results)
}

func main() {
	usage := "usage: generative_benchmarking filter | generate [-heuristic] | evaluate | compare results/*.json"
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "filter":
		filterCmd(ctx, os.Args[2:])
	case "generate":
		generateCmd(ctx, os.Args[2:])
	case "evaluate":
		evaluateCmd(ctx, os.Args[2:])
	case "compare":
		compareCmd(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}
