package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/xen0bit/kaleid/internal/apierr"
)

// Fork copies a collection (records, schema, metadata, index) into a new
// collection in the same database. Chroma Cloud forks copy-on-write; here the
// data is copied inside one transaction, so the fork is immediately isolated.
func (s *Store) Fork(ctx context.Context, src Collection, newName string) (Collection, error) {
	var out Collection
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockCollection(ctx, tx, &src); err != nil {
			return err
		}
		var root *uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT forked_from FROM kaleid.collections WHERE id=$1`, src.ID).Scan(&root); err != nil {
			return err
		}
		lineage := src.ID
		if root != nil {
			lineage = *root
		}
		id := uuid.New()
		schemaJSON, err := json.Marshal(src.Schema)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO kaleid.collections (id, database_id, name, metadata, schema, write_seq, forked_from)
			SELECT $1, database_id, $2, metadata, $3, write_seq, $4 FROM kaleid.collections WHERE id=$5`,
			id, newName, schemaJSON, lineage, src.ID); err != nil {
			if isUniqueViolation(err) {
				return apierr.AlreadyExists("Collection [%s] already exists", newName)
			}
			return err
		}
		if err := createCollectionTables(ctx, tx, id); err != nil {
			return err
		}
		out = src
		out.ID, out.Name, out.Index = id, newName, nil
		dst := dataTable(id)
		if src.Dimension != nil {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`ALTER TABLE %s ALTER COLUMN embedding TYPE vector(%d)`, dst, *src.Dimension)); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET dimension=$2 WHERE id=$1`, id, *src.Dimension); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (rowid, id, embedding, document, uri, metadata, seq)
			SELECT rowid, id, embedding, document, uri, metadata, seq FROM %s ORDER BY rowid`, dst, dataTable(src.ID))); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'rowid'), GREATEST((SELECT max(rowid) FROM %s), 1))`,
			dst, dst)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s SELECT * FROM %s`, sparseTable(id), sparseTable(src.ID))); err != nil {
			return err
		}
		if src.Dimension != nil && src.Index != nil {
			st := *src.Index
			if st.Kind == "vector" || st.Kind == "halfvec" {
				h := src.Schema.ToInternal().Hnsw
				if err := s.buildIndex(ctx, tx, id, *src.Dimension, h); err != nil {
					return err
				}
				st = planIndex(*src.Dimension, h)
			} else if err := setIndexState(ctx, tx, id, st); err != nil {
				return err
			}
			out.Index = &st
		}
		return nil
	})
	return out, err
}

// ForkCount returns the number of forks in the collection's lineage.
func (s *Store) ForkCount(ctx context.Context, c Collection) (int, error) {
	var root *uuid.UUID
	if err := s.pool.QueryRow(ctx, `SELECT forked_from FROM kaleid.collections WHERE id=$1`, c.ID).Scan(&root); err != nil {
		return 0, err
	}
	lineage := c.ID
	if root != nil {
		lineage = *root
	}
	var n int
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM kaleid.collections WHERE forked_from=$1`, lineage).Scan(&n)
	return n, err
}

// WriteSeq returns the number of operations applied to a collection.
func (s *Store) WriteSeq(ctx context.Context, c Collection) (int64, error) {
	var n int64
	err := s.pool.QueryRow(ctx, `SELECT write_seq FROM kaleid.collections WHERE id=$1`, c.ID).Scan(&n)
	return n, err
}

// TenantByResourceName resolves a tenant name from its resource name.
func (s *Store) TenantByResourceName(ctx context.Context, resourceName string) (string, error) {
	var name string
	err := s.pool.QueryRow(ctx, `SELECT name FROM kaleid.tenants WHERE resource_name=$1`, resourceName).Scan(&name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apierr.NotFound("Tenant [%s] not found", resourceName)
	}
	return name, err
}
