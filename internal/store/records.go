package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/wire"
)

// WriteMode selects add / upsert / update semantics.
type WriteMode int

const (
	ModeAdd WriteMode = iota
	ModeUpsert
	ModeUpdate
)

// WriteBatch is a column-oriented batch of record operations. A nil slice
// means the field was not provided; a nil element means "no value" (null).
type WriteBatch struct {
	IDs        []string
	Embeddings [][]float32
	Documents  []*string
	URIs       []*string
	Metadatas  []wire.UpdateMetadata
	// MetadataPresent[i] is false when metadatas[i] was null.
	MetadataPresent []bool
}

type recState struct {
	id       string
	rowid    int64 // 0 for new rows
	isNew    bool
	emb      []float32 // nil = keep existing
	doc      *string
	uri      *string
	md       wire.Metadata
	mdChange bool
	touched  bool
}

// lockCollection takes the collection's write lock and re-reads the row.
func lockCollection(ctx context.Context, tx pgx.Tx, c *Collection) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey(c.ID)); err != nil {
		return err
	}
	fresh, err := scanCollection(tx.QueryRow(ctx, `SELECT `+collectionCols+` FROM kaleid.collections c
		JOIN kaleid.databases d ON d.id=c.database_id WHERE c.id=$1`, c.ID))
	if errors.Is(err, pgx.ErrNoRows) {
		return apierr.NotFound("Collection [%s] does not exist", c.ID)
	}
	if err != nil {
		return err
	}
	*c = fresh
	return nil
}

// Write applies a batch with Chroma's log semantics: operations are applied
// in order; add ignores existing ids, update ignores missing ids, and
// update/upsert merge metadata (null values delete keys).
func (s *Store) Write(ctx context.Context, c Collection, mode WriteMode, b WriteBatch) error {
	return s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockCollection(ctx, tx, &c); err != nil {
			return err
		}
		for _, e := range b.Embeddings {
			if e == nil {
				continue
			}
			if err := s.ensureDimension(ctx, tx, &c, len(e)); err != nil {
				return err
			}
		}
		t := dataTable(c.ID)

		// Load existing rows touched by this batch.
		existing := map[string]*recState{}
		rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT rowid, id, document, uri, metadata FROM %s WHERE id = ANY($1)`, t), b.IDs)
		if err != nil {
			return err
		}
		for rows.Next() {
			st := &recState{}
			var md []byte
			if err := rows.Scan(&st.rowid, &st.id, &st.doc, &st.uri, &md); err != nil {
				rows.Close()
				return err
			}
			if st.md, err = wire.ParseMetadata(md); err != nil {
				rows.Close()
				return err
			}
			existing[st.id] = st
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		var order []*recState
		for i, id := range b.IDs {
			var emb []float32
			if b.Embeddings != nil {
				emb = b.Embeddings[i]
			}
			var doc, uri *string
			if b.Documents != nil {
				doc = b.Documents[i]
			}
			if b.URIs != nil {
				uri = b.URIs[i]
			}
			var upd wire.UpdateMetadata
			mdPresent := b.Metadatas != nil && b.MetadataPresent[i]
			if mdPresent {
				upd = b.Metadatas[i]
			}
			st, ok := existing[id]
			switch {
			case !ok && mode == ModeUpdate:
				continue
			case ok && mode == ModeAdd:
				continue
			case !ok:
				st = &recState{id: id, isNew: true, emb: emb, doc: doc, uri: uri, md: upd.ToMetadata(), mdChange: true, touched: true}
				if len(st.md) == 0 {
					st.md = nil
				}
				existing[id] = st
				order = append(order, st)
			default:
				if !st.touched {
					st.touched = true
					order = append(order, st)
				}
				if emb != nil {
					st.emb = emb
				}
				if doc != nil {
					st.doc = doc
				}
				if uri != nil {
					st.uri = uri
				}
				if mdPresent {
					st.md = st.md.Merge(upd)
					st.mdChange = true
				}
			}
		}
		if len(order) == 0 {
			return nil
		}

		var seq int64
		if err := tx.QueryRow(ctx, `UPDATE kaleid.collections SET write_seq = write_seq + $2 WHERE id=$1 RETURNING write_seq`,
			c.ID, len(order)).Scan(&seq); err != nil {
			return err
		}

		var (
			newIDs  []string
			newEmb  []pgvector.Vector
			newDoc  []*string
			newURI  []*string
			newMD   []string
			updRow  []int64
			updEmb  []*pgvector.Vector
			updDoc  []*string
			updURI  []*string
			updMD   []string
			sparseN []*recState
		)
		for _, st := range order {
			mdJSON := "{}"
			if st.md != nil {
				mdJSON = string(st.md.StorageJSON())
			}
			if st.isNew {
				newIDs = append(newIDs, st.id)
				newEmb = append(newEmb, pgvector.NewVector(st.emb))
				newDoc = append(newDoc, st.doc)
				newURI = append(newURI, st.uri)
				newMD = append(newMD, mdJSON)
			} else {
				updRow = append(updRow, st.rowid)
				if st.emb != nil {
					v := pgvector.NewVector(st.emb)
					updEmb = append(updEmb, &v)
				} else {
					updEmb = append(updEmb, nil)
				}
				updDoc = append(updDoc, st.doc)
				updURI = append(updURI, st.uri)
				updMD = append(updMD, mdJSON)
			}
			if st.mdChange && hasSparse(st.md) {
				sparseN = append(sparseN, st)
			}
		}

		if len(newIDs) > 0 {
			rows, err := tx.Query(ctx, fmt.Sprintf(`INSERT INTO %s (id, embedding, document, uri, metadata, seq)
				SELECT u.id, u.emb, u.doc, u.uri, u.md::jsonb, $6
				FROM unnest($1::text[], $2::vector[], $3::text[], $4::text[], $5::text[]) WITH ORDINALITY AS u(id, emb, doc, uri, md, ord)
				ORDER BY u.ord
				RETURNING rowid, id`, t), newIDs, newEmb, newDoc, newURI, newMD, seq)
			if err != nil {
				return err
			}
			for rows.Next() {
				var rid int64
				var id string
				if err := rows.Scan(&rid, &id); err != nil {
					rows.Close()
					return err
				}
				existing[id].rowid = rid
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
		}
		if len(updRow) > 0 {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE %s AS t SET
					embedding = COALESCE(u.emb, t.embedding), document = u.doc, uri = u.uri, metadata = u.md::jsonb, seq = $6
				FROM unnest($1::bigint[], $2::vector[], $3::text[], $4::text[], $5::text[]) AS u(rid, emb, doc, uri, md)
				WHERE t.rowid = u.rid`, t), updRow, updEmb, updDoc, updURI, updMD, seq); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE rid = ANY($1)`, sparseTable(c.ID)), updRow); err != nil {
				return err
			}
		}
		if err := writePostings(ctx, tx, c, sparseN); err != nil {
			return err
		}
		if err := growSchema(ctx, tx, &c, order); err != nil {
			return err
		}
		return s.maybeBuildIndex(ctx, tx, &c)
	})
}

func hasSparse(md wire.Metadata) bool {
	for _, v := range md {
		if v.Kind == wire.KindSparse {
			return true
		}
	}
	return false
}

// writePostings maintains the per-collection sparse postings used by the
// Search API's sparse KNN and BM25 IDF.
func writePostings(ctx context.Context, tx pgx.Tx, c Collection, recs []*recState) error {
	if len(recs) == 0 {
		return nil
	}
	var keys []string
	var dims, rids []int64
	var vals []float32
	for _, st := range recs {
		for k, v := range st.md {
			if v.Kind != wire.KindSparse {
				continue
			}
			for i, d := range v.Sparse.Indices {
				keys = append(keys, k)
				dims = append(dims, int64(d))
				rids = append(rids, st.rowid)
				vals = append(vals, v.Sparse.Values[i])
			}
		}
	}
	if len(keys) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (key, dim, rid, value)
		SELECT * FROM unnest($1::text[], $2::bigint[], $3::bigint[], $4::real[])
		ON CONFLICT (key, dim, rid) DO UPDATE SET value = EXCLUDED.value`, sparseTable(c.ID)), keys, dims, rids, vals)
	return err
}

// growSchema adds schema key entries for newly observed metadata keys/types.
func growSchema(ctx context.Context, tx pgx.Tx, c *Collection, recs []*recState) error {
	changed := false
	for _, st := range recs {
		if !st.mdChange {
			continue
		}
		for k, v := range st.md {
			if c.Schema.EnsureKey(k, v.TypeName()) {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(c.Schema)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE kaleid.collections SET schema=$2 WHERE id=$1`, c.ID, b)
	return err
}

// Delete removes records selected by ids and/or a filter. With neither, it
// is a no-op (Chroma semantics). Returns the number of deleted records.
func (s *Store) Delete(ctx context.Context, c Collection, ids []string, where filter.Expr, limit *int) (int, error) {
	if ids == nil && where == nil {
		return 0, nil
	}
	var n int
	err := s.withTx(ctx, func(tx pgx.Tx) error {
		if err := lockCollection(ctx, tx, &c); err != nil {
			return err
		}
		args := &filter.Args{}
		conds := []string{"TRUE"}
		if ids != nil {
			conds = append(conds, "id = ANY("+args.Add(ids)+"::text[])")
		}
		if where != nil {
			w, err := filter.Compile(where, filter.DefaultColumns, args)
			if err != nil {
				return err
			}
			conds = append(conds, w)
		}
		lim := ""
		// Chroma ignores limit when deleting by ids alone.
		if limit != nil && where != nil {
			lim = " LIMIT " + args.Add(*limit)
		}
		t := dataTable(c.ID)
		rows, err := tx.Query(ctx, fmt.Sprintf(`DELETE FROM %[1]s WHERE rowid IN (SELECT rowid FROM %[1]s WHERE %[2]s ORDER BY rowid%[3]s) RETURNING rowid`,
			t, strings.Join(conds, " AND "), lim), args.Vals...)
		if err != nil {
			return err
		}
		rids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return err
		}
		n = len(rids)
		// Deleting by ids alone reports the number of ids requested, whether
		// or not they existed (Chroma semantics).
		if where == nil {
			n = len(ids)
		}
		if len(rids) > 0 {
			if _, err := tx.Exec(ctx, fmt.Sprintf(`DELETE FROM %s WHERE rid = ANY($1)`, sparseTable(c.ID)), rids); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE kaleid.collections SET write_seq = write_seq + $2 WHERE id=$1`, c.ID, len(rids)); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}
