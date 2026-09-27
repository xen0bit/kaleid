// Auth: connecting to a Kaleid server that requires a token.
//
// A Go port of Chroma's auth.ipynb. Start Kaleid with token auth:
//
//	KALEID_AUTH_PROVIDER=token KALEID_AUTH_TOKEN=test-token kaleid
//	KALEID_TOKEN=test-token go run ./examples/auth
//
// Kaleid accepts the token as "Authorization: Bearer <token>" or as
// "X-Chroma-Token: <token>", the two headers Chroma's clients send.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
)

func main() {
	ctx := context.Background()
	url := os.Getenv("KALEID_URL")
	if url == "" {
		url = "http://localhost:8000"
	}
	token := os.Getenv("KALEID_TOKEN")
	if token == "" {
		token = "test-token"
	}

	// Public endpoints work without credentials.
	anon := client.New(url)
	_, err := anon.Heartbeat(ctx)
	exampleenv.Must(err)
	v, err := anon.Version(ctx)
	exampleenv.Must(err)
	fmt.Println("heartbeat ok, API version", v)

	// Protected endpoints are refused without a token.
	if _, err := anon.ListCollections(ctx, 0, 0); err != nil {
		fmt.Println("As expected, anonymous access to protected endpoints is refused:", err)
	} else {
		fmt.Println("The server does not require auth (KALEID_AUTH_PROVIDER=none).")
	}

	// A bearer token (Authorization header)...
	bearer := client.New(url, client.WithToken(token))
	id, err := bearer.Identity(ctx)
	exampleenv.Must(err)
	fmt.Printf("Authenticated as %q (tenant %s, databases %v)\n", id.UserID, id.Tenant, id.Databases)
	cols, err := bearer.ListCollections(ctx, 0, 0)
	exampleenv.Must(err)
	fmt.Println("Collections visible with the bearer token:", len(cols))

	// ...or the X-Chroma-Token header.
	xtoken := client.New(url, client.WithChromaToken(token))
	_, err = xtoken.ListCollections(ctx, 0, 0)
	exampleenv.Must(err)
	fmt.Println("X-Chroma-Token works too")
}
