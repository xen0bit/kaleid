package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
)

const (
	// pgvector HNSW limits.
	maxVectorIndexDims  = 2000
	maxHalfvecIndexDims = 4000
	maxVectorDims       = 16000
)

func createCollectionTables(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	t := dataTable(id)
	sql := fmt.Sprintf(`
		CREATE TABLE %[1]s (
			rowid     bigserial PRIMARY KEY,
			id        text NOT NULL,
			embedding vector,
			document  text,
			uri       text,
			metadata  jsonb NOT NULL DEFAULT '{}'::jsonb,
			seq       bigint NOT NULL DEFAULT 0
		);
		CREATE UNIQUE INDEX %[2]s ON %[1]s (id);
		CREATE INDEX %[3]s ON %[1]s USING gin (metadata jsonb_path_ops);
		CREATE INDEX %[4]s ON %[1]s USING gin (document gin_trgm_ops);
		CREATE TABLE %[5]s (
			key   text NOT NULL,
			dim   bigint NOT NULL,
			rid   bigint NOT NULL,
			value real NOT NULL,
			PRIMARY KEY (key, dim, rid)
		);
		CREATE INDEX %[6]s ON %[5]s (rid);`,
		t, indexName(id, "id"), indexName(id, "md"), indexName(id, "doc"),
		sparseTable(id), indexName(id, "sp_rid"))
	_, err := tx.Exec(ctx, sql)
	return err
}

func dropCollectionTables(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	_, err := tx.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s; DROP TABLE IF EXISTS %s`, dataTable(id), sparseTable(id)))
	return err
}

// opClass returns the pgvector operator class suffix and distance operator
// for a Chroma space.
func opClass(space collection.Space) (ops string, op string) {
	switch space {
	case collection.SpaceCosine:
		return "cosine_ops", "<=>"
	case collection.SpaceIP:
		return "ip_ops", "<#>"
	}
	return "l2_ops", "<->"
}

// planIndex decides which index to build for a dimension and HNSW config.
// pgvector requires 2 <= m <= 100, 4 <= ef_construction <= 1000 and
// ef_construction >= 2*m; values are clamped into range.
func planIndex(dim int, h collection.Hnsw) IndexState {
	kind := "vector"
	switch {
	case dim > maxHalfvecIndexDims:
		return IndexState{Kind: "none"}
	case dim > maxVectorIndexDims:
		kind = "halfvec"
	}
	m := min(max(h.MaxNeighbors, 2), 100)
	ef := min(max(h.EfConstruction, 4, 2*m), 1000)
	return IndexState{Kind: kind, M: m, EfConstruction: ef}
}

func setIndexState(ctx context.Context, tx pgx.Tx, id uuid.UUID, st IndexState) error {
	b, _ := json.Marshal(st)
	_, err := tx.Exec(ctx, `UPDATE kaleid.collections SET index_state=$2 WHERE id=$1`, id, b)
	return err
}

// buildIndex (re)creates the ANN index for a collection and records it.
func (s *Store) buildIndex(ctx context.Context, tx pgx.Tx, id uuid.UUID, dim int, h collection.Hnsw) error {
	st := planIndex(dim, h)
	t := dataTable(id)
	name := indexName(id, "ann")
	if _, err := tx.Exec(ctx, fmt.Sprintf(`DROP INDEX IF EXISTS kaleid_data.%s`, name)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL maintenance_work_mem = '%s'`, strings.ReplaceAll(s.opts.IndexBuildMemory, "'", ""))); err != nil {
		return err
	}
	ops, _ := opClass(h.Space)
	var create string
	switch st.Kind {
	case "vector":
		create = fmt.Sprintf(`CREATE INDEX %s ON %s USING hnsw (embedding vector_%s) WITH (m = %d, ef_construction = %d)`,
			name, t, ops, st.M, st.EfConstruction)
	case "halfvec":
		create = fmt.Sprintf(`CREATE INDEX %s ON %s USING hnsw ((embedding::halfvec(%d)) halfvec_%s) WITH (m = %d, ef_construction = %d)`,
			name, t, dim, ops, st.M, st.EfConstruction)
	}
	if create != "" {
		if err := createIndexWithFallback(ctx, tx, create); err != nil {
			return err
		}
	}
	return setIndexState(ctx, tx, id, st)
}

// maybeBuildIndex builds the deferred index once the collection is large
// enough.
func (s *Store) maybeBuildIndex(ctx context.Context, tx pgx.Tx, c *Collection) error {
	if c.Index == nil || c.Index.Kind != "pending" || c.Dimension == nil {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, dataTable(c.ID))).Scan(&n); err != nil {
		return err
	}
	if n < s.opts.IndexThreshold {
		return nil
	}
	h := c.Schema.ToInternal().Hnsw
	if err := s.buildIndex(ctx, tx, c.ID, *c.Dimension, h); err != nil {
		return err
	}
	st := planIndex(*c.Dimension, h)
	c.Index = &st
	return nil
}

// ensureDimension fixes the collection dimension on the first write carrying
// embeddings: it types the vector column and schedules (or builds) the ANN
// index. The caller holds the collection's write lock.
func (s *Store) ensureDimension(ctx context.Context, tx pgx.Tx, c *Collection, dim int) error {
	if c.Dimension != nil {
		if *c.Dimension != dim {
			return apierr.InvalidArgument("Collection expecting embedding with dimension of %d, got %d", *c.Dimension, dim)
		}
		return nil
	}
	if dim < 1 || dim > maxVectorDims {
		return apierr.InvalidArgument("Embedding dimension %d is not supported (must be between 1 and %d)", dim, maxVectorDims)
	}
	t := dataTable(c.ID)
	if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding TYPE vector(%d)`, t, dim)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET dimension=$2 WHERE id=$1`, c.ID, dim); err != nil {
		return err
	}
	c.Dimension = &dim
	st := IndexState{Kind: "pending"}
	if planIndex(dim, c.Schema.ToInternal().Hnsw).Kind == "none" {
		st = IndexState{Kind: "none"}
	}
	if err := setIndexState(ctx, tx, c.ID, st); err != nil {
		return err
	}
	c.Index = &st
	return nil
}

// createIndexWithFallback runs CREATE INDEX, retrying as a serial build when
// a parallel build cannot allocate dynamic shared memory (SQLSTATE 53100,
// typical of containers with a small /dev/shm).
func createIndexWithFallback(ctx context.Context, tx pgx.Tx, create string) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT kaleid_index_build`); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, create)
	if err == nil {
		_, err = tx.Exec(ctx, `RELEASE SAVEPOINT kaleid_index_build`)
		return err
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "53100" {
		return err
	}
	if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT kaleid_index_build; SET LOCAL max_parallel_maintenance_workers = 0`); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, create)
	return err
}
