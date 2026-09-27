// Package exampleenv holds small helpers shared by the examples and sample
// apps: connecting to a server and choosing an embedder from the environment.
package exampleenv

import (
	"fmt"
	"os"

	"github.com/xen0bit/kaleid/pkg/client"
	"github.com/xen0bit/kaleid/pkg/embed"
)

// Client connects to KALEID_URL (default http://localhost:8000), using
// KALEID_TOKEN as a bearer token when set. It works against Chroma too.
func Client() *client.Client {
	url := os.Getenv("KALEID_URL")
	if url == "" {
		url = "http://localhost:8000"
	}
	var opts []client.Option
	if tok := os.Getenv("KALEID_TOKEN"); tok != "" {
		opts = append(opts, client.WithToken(tok))
	}
	return client.New(url, opts...)
}

// Embedder returns the embedder selected by EMBEDDER (see embed.FromEnv).
func Embedder() embed.Embedder {
	e, err := embed.FromEnv()
	Must(err)
	return e
}

// Must exits with a message on error.
func Must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
