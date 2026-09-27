// Command kaleid runs a Chroma-compatible vector database server backed by
// PostgreSQL + pgvector.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/xen0bit/kaleid/internal/api"
	"github.com/xen0bit/kaleid/internal/auth"
	"github.com/xen0bit/kaleid/internal/store"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

type options struct {
	databaseURL   string
	listen        string
	allowReset    bool
	maxBatch      int
	maxPayload    int64
	cors          string
	authProvider  string
	authToken     string
	authFile      string
	exactFallback bool
	maxScan       int
	indexThresh   int
	indexMem      string
	logLevel      string
}

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("kaleid", flag.ContinueOnError)
	fs.StringVar(&o.databaseURL, "database-url", env("KALEID_DATABASE_URL", ""), "PostgreSQL connection URL (env KALEID_DATABASE_URL)")
	fs.StringVar(&o.listen, "listen", env("KALEID_LISTEN", ":8000"), "listen address (env KALEID_LISTEN)")
	fs.BoolVar(&o.allowReset, "allow-reset", envBool("KALEID_ALLOW_RESET", false), "enable POST /api/v2/reset (env KALEID_ALLOW_RESET)")
	fs.IntVar(&o.maxBatch, "max-batch-size", envInt("KALEID_MAX_BATCH_SIZE", 5461), "max_batch_size reported to clients (env KALEID_MAX_BATCH_SIZE)")
	payload := fs.Int("max-payload-bytes", envInt("KALEID_MAX_PAYLOAD_BYTES", 40<<20), "maximum request body size (env KALEID_MAX_PAYLOAD_BYTES)")
	fs.StringVar(&o.cors, "cors-allow-origins", env("KALEID_CORS_ALLOW_ORIGINS", ""), "comma-separated CORS origins, or * (env KALEID_CORS_ALLOW_ORIGINS)")
	fs.StringVar(&o.authProvider, "auth", env("KALEID_AUTH_PROVIDER", "none"), "auth provider: none | token (env KALEID_AUTH_PROVIDER)")
	fs.StringVar(&o.authToken, "auth-token", env("KALEID_AUTH_TOKEN", ""), "single full-access token for token auth (env KALEID_AUTH_TOKEN)")
	fs.StringVar(&o.authFile, "auth-tokens-file", env("KALEID_AUTH_TOKENS_FILE", ""), "JSON token file for token auth (env KALEID_AUTH_TOKENS_FILE)")
	fs.BoolVar(&o.exactFallback, "exact-fallback", envBool("KALEID_EXACT_FALLBACK", true), "re-run short filtered KNN results as exact scans (env KALEID_EXACT_FALLBACK)")
	fs.IntVar(&o.maxScan, "max-scan-tuples", envInt("KALEID_MAX_SCAN_TUPLES", 20000), "hnsw.max_scan_tuples for iterative scans (env KALEID_MAX_SCAN_TUPLES)")
	fs.IntVar(&o.indexThresh, "index-threshold", envInt("KALEID_INDEX_THRESHOLD", 10000), "record count at which a collection's HNSW index is built; 0 = immediately (env KALEID_INDEX_THRESHOLD)")
	fs.StringVar(&o.indexMem, "index-build-memory", env("KALEID_INDEX_BUILD_MEMORY", "256MB"), "maintenance_work_mem for HNSW index builds (env KALEID_INDEX_BUILD_MEMORY)")
	fs.StringVar(&o.logLevel, "log-level", env("KALEID_LOG_LEVEL", "info"), "log level: debug | info | warn | error (env KALEID_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	o.maxPayload = int64(*payload)
	return o, nil
}

func buildAuth(o options) (auth.Provider, error) {
	switch o.authProvider {
	case "", "none":
		return auth.None{}, nil
	case "token":
		var entries []auth.TokenEntry
		if o.authFile != "" {
			fileEntries, err := auth.LoadTokenFile(o.authFile)
			if err != nil {
				return nil, err
			}
			entries = append(entries, fileEntries...)
		}
		if o.authToken != "" {
			entries = append(entries, auth.TokenEntry{Token: o.authToken, UserID: "admin"})
		}
		return auth.NewToken(entries)
	}
	return nil, fmt.Errorf("unknown auth provider %q", o.authProvider)
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "kaleid:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "serve"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	switch cmd {
	case "version":
		fmt.Println(version)
		return nil
	case "serve", "migrate":
	default:
		return fmt.Errorf("unknown command %q (expected serve, migrate or version)", cmd)
	}
	o, err := parseOptions(args)
	if err != nil {
		return err
	}
	if o.databaseURL == "" {
		return errors.New("a database URL is required (--database-url or KALEID_DATABASE_URL)")
	}
	log := newLogger(o.logLevel)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, o.databaseURL, store.Options{
		MaxScanTuples: o.maxScan, ExactFallback: o.exactFallback, IndexThreshold: o.indexThresh, IndexBuildMemory: o.indexMem,
	})
	if err != nil {
		return err
	}
	defer st.Close()
	if cmd == "migrate" {
		log.Info("migrations applied")
		return nil
	}
	provider, err := buildAuth(o)
	if err != nil {
		return err
	}
	var origins []string
	for _, o := range strings.Split(o.cors, ",") {
		if o = strings.TrimSpace(o); o != "" {
			origins = append(origins, o)
		}
	}
	srv := api.New(st, provider, api.Config{
		AllowReset: o.allowReset, MaxBatchSize: o.maxBatch, MaxPayloadBytes: o.maxPayload, CORSOrigins: origins,
	}, log)
	httpSrv := &http.Server{Addr: o.listen, Handler: srv.Handler(), ReadHeaderTimeout: 30 * time.Second}
	errCh := make(chan error, 1)
	go func() {
		log.Info("kaleid listening", "addr", o.listen, "version", version, "auth", o.authProvider)
		errCh <- httpSrv.ListenAndServe()
	}()
	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdownCtx)
	}
	return nil
}
