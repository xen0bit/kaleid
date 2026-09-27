// Chat with your documents: retrieval-augmented generation over local text.
//
// A Go port of Chroma's examples/chat_with_your_documents (and its gemini
// and xai variants, which differ only in the LLM provider). Each non-empty
// line of the files in ./documents becomes a record; a question retrieves
// the most relevant lines, which are passed to an LLM as context.
//
//	# 1. Load the documents (the 2022 and 2023 State of the Union addresses)
//	go run ./examples/chat_with_your_documents load
//
//	# 2. Chat (OpenAI by default; see -provider)
//	OPENAI_API_KEY=... go run ./examples/chat_with_your_documents chat
//	LLM_PROVIDER=ollama go run ./examples/chat_with_your_documents chat
//	LLM_PROVIDER=gemini GEMINI_API_KEY=... go run ./examples/chat_with_your_documents chat
//	LLM_PROVIDER=xai XAI_API_KEY=... go run ./examples/chat_with_your_documents chat
//
//	# Retrieval only, no LLM needed
//	go run ./examples/chat_with_your_documents chat -no-llm -question "What did the president say about inflation?"
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/internal/llm"
	"github.com/xen0bit/kaleid/pkg/client"
)

const systemPrompt = "I am going to ask you a question, which I would like you to answer " +
	"based only on the provided context, and not any other information. " +
	"If there is not enough information in the context to answer the question, " +
	`say "I am not sure", then try to make a guess. ` +
	"Break your answer up into nicely readable paragraphs."

func defaultDocsDir() string {
	// Works from the repo root and from the example's own directory.
	for _, d := range []string{"examples/chat_with_your_documents/documents", "documents"} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return "documents"
}

func load(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	dir := fs.String("dir", defaultDocsDir(), "directory of .txt documents")
	name := fs.String("collection", "documents_collection", "collection name")
	reset := fs.Bool("reset", false, "delete the collection first")
	_ = fs.Parse(args)

	c := exampleenv.Client()
	emb := exampleenv.Embedder()
	if *reset {
		if err := c.DeleteCollection(ctx, *name); err != nil && !client.IsNotFound(err) {
			exampleenv.Must(err)
		}
	}
	col, err := c.CreateCollection(ctx, *name, &client.CreateCollectionOptions{
		HNSW: &client.HNSWConfig{Space: client.SpaceCosine}, GetOrCreate: true,
	})
	exampleenv.Must(err)

	files, err := filepath.Glob(filepath.Join(*dir, "*.txt"))
	exampleenv.Must(err)
	sort.Strings(files)
	var docs []string
	var mds []client.Metadata
	for _, f := range files {
		fh, err := os.Open(f)
		exampleenv.Must(err)
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		line := 0
		for sc.Scan() {
			line++
			text := strings.TrimSpace(sc.Text())
			if text == "" {
				continue
			}
			docs = append(docs, text)
			mds = append(mds, client.Metadata{"filename": filepath.Base(f), "line_number": line})
		}
		fh.Close()
	}

	count, err := col.Count(ctx)
	exampleenv.Must(err)
	fmt.Printf("Collection already contains %d documents\n", count)
	const batch = 100
	for i := 0; i < len(docs); i += batch {
		end := min(i+batch, len(docs))
		ids := make([]string, 0, end-i)
		for j := i; j < end; j++ {
			ids = append(ids, strconv.Itoa(count+j))
		}
		vecs, err := emb.Embed(ctx, docs[i:end])
		exampleenv.Must(err)
		exampleenv.Must(col.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: docs[i:end], Metadatas: mds[i:end]}))
	}
	after, err := col.Count(ctx)
	exampleenv.Must(err)
	fmt.Printf("Added %d documents\n", after-count)
}

func chat(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("chat", flag.ExitOnError)
	name := fs.String("collection", "documents_collection", "collection name")
	question := fs.String("question", "", "ask one question and exit")
	noLLM := fs.Bool("no-llm", false, "print the retrieved context instead of calling an LLM")
	nResults := fs.Int("n", 5, "context lines to retrieve")
	_ = fs.Parse(args)

	c := exampleenv.Client()
	emb := exampleenv.Embedder()
	col, err := c.GetCollection(ctx, *name)
	if client.IsNotFound(err) {
		exampleenv.Must(fmt.Errorf("collection %q not found; run the load step first", *name))
	}
	exampleenv.Must(err)

	var model *llm.Client
	if !*noLLM {
		model, err = llm.FromEnv()
		exampleenv.Must(err)
		fmt.Printf("Using %s model %s\n", model.Provider, model.Model)
	}

	ask := func(q string) {
		qv, err := emb.Embed(ctx, []string{q})
		exampleenv.Must(err)
		res, err := col.Query(ctx, client.QueryOptions{Embeddings: qv, NResults: *nResults,
			Include: []client.Include{client.IncludeDocuments, client.IncludeMetadatas}})
		exampleenv.Must(err)
		var context []string
		for _, d := range res.Documents[0] {
			context = append(context, *d)
		}
		if model != nil {
			user := fmt.Sprintf("The question is %s. Here is all the context you have: %s", q, strings.Join(context, " "))
			answer, err := model.Complete(ctx, systemPrompt, user)
			exampleenv.Must(err)
			fmt.Printf("\n%s\n", answer)
		} else {
			fmt.Println("\nRetrieved context:")
			for _, line := range context {
				fmt.Println("  -", line)
			}
		}
		fmt.Println("\nSource documents:")
		for _, md := range res.Metadatas[0] {
			fmt.Printf("  %v: line %v\n", md["filename"], md["line_number"])
		}
		fmt.Println()
	}

	if *question != "" {
		ask(*question)
		return
	}
	in := bufio.NewScanner(os.Stdin)
	for {
		fmt.Print("Query: ")
		if !in.Scan() {
			return
		}
		q := strings.TrimSpace(in.Text())
		if q == "" {
			continue
		}
		ask(q)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: chat_with_your_documents load|chat [flags]")
		os.Exit(2)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "load":
		load(ctx, os.Args[2:])
	case "chat":
		chat(ctx, os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "usage: chat_with_your_documents load|chat [flags]")
		os.Exit(2)
	}
}
