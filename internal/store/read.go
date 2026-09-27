package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/wire"
)

// Include selects optional result fields.
type Include struct {
	Embeddings, Documents, Metadatas, URIs, Distances bool
}

// Record is a stored record.
type Record struct {
	RowID     int64
	ID        string
	Embedding []float32
	Document  *string
	URI       *string
	Metadata  wire.Metadata
	Distance  float32
}

func selectCols(inc Include, alias string) string {
	p := alias
	if p != "" {
		p += "."
	}
	cols := []string{p + "rowid", p + "id"}
	if inc.Embeddings {
		cols = append(cols, p+"embedding")
	} else {
		cols = append(cols, "NULL::vector")
	}
	if inc.Documents {
		cols = append(cols, p+"document")
	} else {
		cols = append(cols, "NULL::text")
	}
	if inc.URIs {
		cols = append(cols, p+"uri")
	} else {
		cols = append(cols, "NULL::text")
	}
	if inc.Metadatas {
		cols = append(cols, p+"metadata")
	} else {
		cols = append(cols, "NULL::jsonb")
	}
	return strings.Join(cols, ", ")
}

func scanRecord(rows pgx.Rows, withDistance bool) (Record, error) {
	var r Record
	var emb *pgvector.Vector
	var md []byte
	dest := []any{&r.RowID, &r.ID, &emb, &r.Document, &r.URI, &md}
	var dist float64
	if withDistance {
		dest = append(dest, &dist)
	}
	if err := rows.Scan(dest...); err != nil {
		return r, err
	}
	if emb != nil {
		r.Embedding = emb.Slice()
	}
	if md != nil {
		m, err := wire.ParseMetadata(md)
		if err != nil {
			return r, err
		}
		if len(m) > 0 {
			r.Metadata = m
		}
	}
	r.Distance = float32(dist)
	return r, nil
}

func buildConds(ids []string, where filter.Expr, args *filter.Args, cols filter.Columns) ([]string, error) {
	conds := []string{"TRUE"}
	if ids != nil {
		conds = append(conds, cols.ID+" = ANY("+args.Add(ids)+"::text[])")
	}
	if where != nil {
		w, err := filter.Compile(where, cols, args)
		if err != nil {
			return nil, err
		}
		conds = append(conds, w)
	}
	return conds, nil
}

// Get returns records matching ids and/or filter in insertion order.
func (s *Store) Get(ctx context.Context, c Collection, ids []string, where filter.Expr, limit *int, offset int, inc Include) ([]Record, error) {
	args := &filter.Args{}
	conds, err := buildConds(ids, where, args, filter.DefaultColumns)
	if err != nil {
		return nil, err
	}
	sql := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY rowid`, selectCols(inc, ""), dataTable(c.ID), strings.Join(conds, " AND "))
	if limit != nil {
		sql += " LIMIT " + args.Add(*limit)
	}
	if offset > 0 {
		sql += " OFFSET " + args.Add(offset)
	}
	rows, err := s.pool.Query(ctx, sql, args.Vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Count returns the number of records in a collection.
func (s *Store) Count(ctx context.Context, c Collection) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, dataTable(c.ID))).Scan(&n)
	return n, err
}

// distanceExpr returns SQL for Chroma's distance given the raw pgvector
// operator result expression.
func distanceExpr(space collection.Space, raw string) string {
	switch space {
	case collection.SpaceL2:
		// Chroma reports squared L2.
		return fmt.Sprintf("((%s) * (%s))", raw, raw)
	case collection.SpaceIP:
		// Chroma reports 1 - dot; pgvector's <#> is -dot.
		return fmt.Sprintf("(1 + (%s))", raw)
	}
	return raw
}

type knnPlan struct {
	sql  string
	args []any
}

// planKNN builds a KNN query. exact forces a sequential scan (the ORDER BY
// expression no longer matches the index expression).
func planKNN(c Collection, q []float32, n int, conds []string, baseArgs []any, inc Include, exact bool) knnPlan {
	space := c.Schema.ToInternal().Hnsw.Space
	_, op := opClass(space)
	args := append([]any{}, baseArgs...)
	qp := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, pgvector.NewVector(q))
	raw := fmt.Sprintf("embedding %s %s", op, qp)
	dist := distanceExpr(space, raw)
	t := dataTable(c.ID)
	where := strings.Join(append([]string{"embedding IS NOT NULL"}, conds...), " AND ")
	cols := selectCols(inc, "")
	lim := fmt.Sprintf("$%d", len(args)+1)
	args = append(args, n)

	kind := "none"
	if c.Index != nil {
		kind = c.Index.Kind
	}
	switch {
	case exact || kind == "none" || kind == "pending":
		return knnPlan{fmt.Sprintf(`SELECT %s, %s AS dist FROM %s WHERE %s ORDER BY (%s) + 0, rowid LIMIT %s`,
			cols, dist, t, where, raw, lim), args}
	case kind == "halfvec":
		d := *c.Dimension
		cand := fmt.Sprintf("$%d", len(args)+1)
		args = append(args, max(n*4, n+40))
		return knnPlan{fmt.Sprintf(`SELECT * FROM (SELECT %s, %s AS dist FROM %s WHERE %s
			ORDER BY embedding::halfvec(%d) %s %s::halfvec(%d) LIMIT %s) s ORDER BY dist, rowid LIMIT %s`,
			cols, dist, t, where, d, op, qp, d, cand, lim), args}
	}
	return knnPlan{fmt.Sprintf(`SELECT %s, %s AS dist FROM %s WHERE %s ORDER BY %s LIMIT %s`,
		cols, dist, t, where, raw, lim), args}
}

func (s *Store) runKNN(ctx context.Context, tx pgx.Tx, p knnPlan) ([]Record, error) {
	rows, err := tx.Query(ctx, p.sql, p.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		r, err := scanRecord(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Query runs one KNN search per query embedding.
func (s *Store) Query(ctx context.Context, c Collection, queries [][]float32, n int, ids []string, where filter.Expr, inc Include) ([][]Record, error) {
	out := make([][]Record, len(queries))
	if c.Dimension != nil {
		for _, q := range queries {
			if len(q) != *c.Dimension {
				return nil, apierr.InvalidArgument("Collection expecting embedding with dimension of %d, got %d", *c.Dimension, len(q))
			}
		}
	}
	if n == 0 || c.Dimension == nil || len(queries) == 0 {
		return out, nil
	}
	args := &filter.Args{}
	conds, err := buildConds(ids, where, args, filter.DefaultColumns)
	if err != nil {
		return nil, err
	}
	efSearch := min(max(c.Schema.ToInternal().Hnsw.EfSearch, n, 1), 1000)
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d; SET LOCAL hnsw.iterative_scan = strict_order; SET LOCAL hnsw.max_scan_tuples = %d`,
			efSearch, s.opts.MaxScanTuples)); err != nil {
			return err
		}
		// Restricting to explicit ids is always cheaper as an exact scan, and
		// collections without a built index are always scanned exactly.
		exact := ids != nil || c.Index == nil || c.Index.Kind != "vector" && c.Index.Kind != "halfvec"
		matching := -1
		for i, q := range queries {
			recs, err := s.runKNN(ctx, tx, planKNN(c, q, n, conds[1:], args.Vals, inc, exact))
			if err != nil {
				return err
			}
			if !exact && len(recs) < n && s.opts.ExactFallback {
				if matching < 0 {
					if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE embedding IS NOT NULL AND %s`,
						dataTable(c.ID), strings.Join(conds, " AND ")), args.Vals...).Scan(&matching); err != nil {
						return err
					}
				}
				if matching > len(recs) {
					if recs, err = s.runKNN(ctx, tx, planKNN(c, q, n, conds[1:], args.Vals, inc, true)); err != nil {
						return err
					}
				}
			}
			// Break distance ties by insertion order, as Chroma does.
			sort.SliceStable(recs, func(a, b int) bool {
				if recs[a].Distance != recs[b].Distance {
					return recs[a].Distance < recs[b].Distance
				}
				return recs[a].RowID < recs[b].RowID
			})
			out[i] = recs
		}
		return nil
	})
	return out, err
}
