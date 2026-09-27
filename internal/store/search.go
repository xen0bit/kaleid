package store

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/collection"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/search"
	"github.com/xen0bit/kaleid/internal/wire"
)

// SearchRecord is one search hit.
type SearchRecord struct {
	ID        string
	Document  *string
	Embedding []float32
	Metadata  wire.Metadata
	Score     *float32
}

// Search executes one search payload.
func (s *Store) Search(ctx context.Context, c Collection, p search.Payload) ([]SearchRecord, error) {
	args := &filter.Args{}
	conds, err := buildConds(nil, p.Filter, args, filter.DefaultColumns)
	if err != nil {
		return nil, err
	}
	var out []SearchRecord
	err = s.withTx(ctx, func(tx pgx.Tx) error {
		var ranked []search.Measure
		scored := p.Rank != nil
		if !scored {
			sql := fmt.Sprintf(`SELECT rowid FROM %s WHERE %s ORDER BY rowid`, dataTable(c.ID), strings.Join(conds, " AND "))
			a := append([]any{}, args.Vals...)
			if p.Limit != nil {
				a = append(a, *p.Limit)
				sql += fmt.Sprintf(" LIMIT $%d", len(a))
			}
			if p.Offset > 0 {
				a = append(a, p.Offset)
				sql += fmt.Sprintf(" OFFSET $%d", len(a))
			}
			rows, err := tx.Query(ctx, sql, a...)
			if err != nil {
				return err
			}
			ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
			if err != nil {
				return err
			}
			for _, id := range ids {
				ranked = append(ranked, search.Measure{RowID: id})
			}
		} else {
			leaves := p.Rank.KnnLeaves()
			results := make([][]search.Measure, len(leaves))
			for i, k := range leaves {
				res, err := s.searchKnn(ctx, tx, c, k, conds, args.Vals)
				if err != nil {
					return err
				}
				results[i] = res
			}
			ranked = search.Evaluate(p.Rank, results)
			if p.GroupBy.Active() {
				meta, err := fetchMetadata(ctx, tx, c, ranked)
				if err != nil {
					return err
				}
				ranked = search.GroupRecords(p.GroupBy, ranked, meta)
			}
			if p.Offset >= len(ranked) {
				ranked = nil
			} else {
				ranked = ranked[p.Offset:]
			}
			if p.Limit != nil && len(ranked) > *p.Limit {
				ranked = ranked[:*p.Limit]
			}
		}
		recs, err := fetchSelected(ctx, tx, c, ranked, p.Select)
		if err != nil {
			return err
		}
		out = make([]SearchRecord, len(ranked))
		for i, m := range ranked {
			r := recs[m.RowID]
			if scored {
				score := m.Score
				r.Score = &score
			}
			out[i] = r
		}
		return nil
	})
	return out, err
}

func (s *Store) searchKnn(ctx context.Context, tx pgx.Tx, c Collection, k *search.Knn, conds []string, baseArgs []any) ([]search.Measure, error) {
	if k.Limit == 0 {
		return nil, nil
	}
	if k.Key == search.KeyEmbedding {
		if k.Dense == nil {
			return nil, apierr.InvalidArgument("Sparse vector queries are only supported on sparse vector keys, not %s", k.Key)
		}
		if c.Dimension == nil {
			return nil, nil
		}
		if len(k.Dense) != *c.Dimension {
			return nil, apierr.InvalidArgument("Collection expecting embedding with dimension of %d, got %d", *c.Dimension, len(k.Dense))
		}
		efSearch := min(max(c.Schema.ToInternal().Hnsw.EfSearch, k.Limit, 1), 1000)
		if _, err := tx.Exec(ctx, fmt.Sprintf(`SET LOCAL hnsw.ef_search = %d; SET LOCAL hnsw.iterative_scan = strict_order; SET LOCAL hnsw.max_scan_tuples = %d`,
			efSearch, s.opts.MaxScanTuples)); err != nil {
			return nil, err
		}
		indexed := c.Index != nil && (c.Index.Kind == "vector" || c.Index.Kind == "halfvec")
		recs, err := s.runKNN(ctx, tx, planKNN(c, k.Dense, k.Limit, conds[1:], baseArgs, Include{}, !indexed))
		if err != nil {
			return nil, err
		}
		if indexed && len(recs) < k.Limit && s.opts.ExactFallback {
			var matching int
			if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s WHERE embedding IS NOT NULL AND %s`,
				dataTable(c.ID), strings.Join(conds, " AND ")), baseArgs...).Scan(&matching); err != nil {
				return nil, err
			}
			if matching > len(recs) {
				if recs, err = s.runKNN(ctx, tx, planKNN(c, k.Dense, k.Limit, conds[1:], baseArgs, Include{}, true)); err != nil {
					return nil, err
				}
			}
		}
		out := make([]search.Measure, len(recs))
		for i, r := range recs {
			out[i] = search.Measure{RowID: r.RowID, Score: r.Distance}
		}
		search.SortMeasures(out)
		return out, nil
	}
	if k.Sparse == nil {
		return nil, apierr.InvalidArgument("Dense vector queries are only supported on the %s key, not %s", search.KeyEmbedding, k.Key)
	}
	q := k.Sparse.Normalized()
	weights := make([]float32, len(q.Values))
	copy(weights, q.Values)
	if isBM25(c.Schema, k.Key) {
		if err := applyIDF(ctx, tx, c, k.Key, q.Indices, weights); err != nil {
			return nil, err
		}
	}
	dims := make([]int64, len(q.Indices))
	for i, d := range q.Indices {
		dims[i] = int64(d)
	}
	a := append([]any{}, baseArgs...)
	n := len(a)
	a = append(a, dims, weights, k.Key, k.Limit)
	sql := fmt.Sprintf(`SELECT p.rid, (1 - sum(p.value * q.w))::float8 AS m
		FROM %s p
		JOIN unnest($%d::bigint[], $%d::real[]) AS q(dim, w) ON p.dim = q.dim
		JOIN %s t ON t.rowid = p.rid
		WHERE p.key = $%d AND %s
		GROUP BY p.rid ORDER BY m, p.rid LIMIT $%d`,
		sparseTable(c.ID), n+1, n+2, dataTable(c.ID), n+3, strings.Join(conds, " AND "), n+4)
	rows, err := tx.Query(ctx, sql, a...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []search.Measure
	for rows.Next() {
		var rid int64
		var m float64
		if err := rows.Scan(&rid, &m); err != nil {
			return nil, err
		}
		out = append(out, search.Measure{RowID: rid, Score: float32(m)})
	}
	return out, rows.Err()
}

func isBM25(s *collection.Schema, key string) bool {
	vt := s.Keys[key]
	if vt == nil || vt.SparseVector == nil || vt.SparseVector.SparseVectorIndex == nil {
		return false
	}
	b := vt.SparseVector.SparseVectorIndex.Config.Bm25
	return b != nil && *b
}

// applyIDF scales query weights by BM25 inverse document frequency:
// idf(t) = ln((n - n_t + 0.5) / (n_t + 0.5) + 1).
func applyIDF(ctx context.Context, tx pgx.Tx, c Collection, key string, indices []uint32, weights []float32) error {
	var n int64
	if err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %s`, dataTable(c.ID))).Scan(&n); err != nil {
		return err
	}
	dims := make([]int64, len(indices))
	for i, d := range indices {
		dims[i] = int64(d)
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT dim, count(*) FROM %s WHERE key = $1 AND dim = ANY($2) GROUP BY dim`, sparseTable(c.ID)), key, dims)
	if err != nil {
		return err
	}
	defer rows.Close()
	nts := map[int64]int64{}
	for rows.Next() {
		var d, cnt int64
		if err := rows.Scan(&d, &cnt); err != nil {
			return err
		}
		nts[d] = cnt
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i, d := range dims {
		nt := float64(min(nts[d], n))
		idf := math.Log1p((float64(n) - nt + 0.5) / (nt + 0.5))
		weights[i] = float32(idf) * weights[i]
	}
	return nil
}

func rowIDs(ms []search.Measure) []int64 {
	out := make([]int64, len(ms))
	for i, m := range ms {
		out[i] = m.RowID
	}
	return out
}

func fetchMetadata(ctx context.Context, tx pgx.Tx, c Collection, ms []search.Measure) (map[int64]wire.Metadata, error) {
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT rowid, metadata FROM %s WHERE rowid = ANY($1)`, dataTable(c.ID)), rowIDs(ms))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]wire.Metadata{}
	for rows.Next() {
		var rid int64
		var md []byte
		if err := rows.Scan(&rid, &md); err != nil {
			return nil, err
		}
		m, err := wire.ParseMetadata(md)
		if err != nil {
			return nil, err
		}
		out[rid] = m
	}
	return out, rows.Err()
}

// fetchSelected loads the projected fields for the ranked records.
func fetchSelected(ctx context.Context, tx pgx.Tx, c Collection, ms []search.Measure, sel []string) (map[int64]SearchRecord, error) {
	out := map[int64]SearchRecord{}
	if len(ms) == 0 {
		return out, nil
	}
	var wantDoc, wantEmb, wantAllMeta bool
	var fields []string
	for _, k := range sel {
		switch k {
		case search.KeyDocument:
			wantDoc = true
		case search.KeyEmbedding:
			wantEmb = true
		case search.KeyMetadata:
			wantAllMeta = true
		case search.KeyScore:
		default:
			fields = append(fields, k)
		}
	}
	wantMeta := wantAllMeta || len(fields) > 0
	cols := []string{"rowid", "id", "NULL::text", "NULL::vector", "NULL::jsonb"}
	if wantDoc {
		cols[2] = "document"
	}
	if wantEmb {
		cols[3] = "embedding"
	}
	if wantMeta {
		cols[4] = "metadata"
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE rowid = ANY($1)`, strings.Join(cols, ", "), dataTable(c.ID)), rowIDs(ms))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var rid int64
		var r SearchRecord
		var emb *pgvector.Vector
		var md []byte
		if err := rows.Scan(&rid, &r.ID, &r.Document, &emb, &md); err != nil {
			return nil, err
		}
		if emb != nil {
			r.Embedding = emb.Slice()
		}
		if md != nil {
			m, err := wire.ParseMetadata(md)
			if err != nil {
				return nil, err
			}
			if !wantAllMeta {
				proj := wire.Metadata{}
				for _, f := range fields {
					if v, ok := m[f]; ok {
						proj[f] = v
					}
				}
				m = proj
			}
			if len(m) > 0 {
				r.Metadata = m
			}
		}
		out[rid] = r
	}
	return out, rows.Err()
}
