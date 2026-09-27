package api

import (
	"encoding/json"
	"net/http"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/search"
	"github.com/xen0bit/kaleid/internal/store"
	"github.com/xen0bit/kaleid/internal/wire"
)

func (s *Server) search(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	var p struct {
		Searches  *[]json.RawMessage `json:"searches"`
		ReadLevel *string            `json:"read_level"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if p.Searches == nil {
		return apierr.Unprocessable("missing field `searches`")
	}
	if p.ReadLevel != nil {
		switch *p.ReadLevel {
		case "index_and_wal", "index_only", "index_and_bounded_wal":
		default:
			return apierr.Unprocessable("read_level: unknown variant `%s`, expected one of `index_and_wal`, `index_only`, `index_and_bounded_wal`", *p.ReadLevel)
		}
	}
	payloads := make([]search.Payload, len(*p.Searches))
	for i, raw := range *p.Searches {
		pl, err := search.ParsePayload(raw, i)
		if err != nil {
			return err
		}
		if err := checkFilterIndexes(c.Schema, pl.Filter); err != nil {
			return err
		}
		payloads[i] = pl
	}
	results := make([][]store.SearchRecord, len(payloads))
	for i, pl := range payloads {
		res, err := s.store.Search(r.Context(), c, pl)
		if err != nil {
			return err
		}
		results[i] = res
	}
	writeRaw(w, http.StatusOK, encodeSearchResponse(payloads, results))
	return nil
}

func has(keys []string, k string) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

func encodeSearchResponse(payloads []search.Payload, results [][]store.SearchRecord) []byte {
	buf := []byte(`{"ids":[`)
	for i, recs := range results {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '[')
		for j, r := range recs {
			if j > 0 {
				buf = append(buf, ',')
			}
			buf = appendStrPtr(buf, &r.ID)
		}
		buf = append(buf, ']')
	}
	buf = append(buf, ']')
	column := func(name string, enabled func(search.Payload) bool, each func([]byte, store.SearchRecord) []byte) {
		buf = append(buf, `,"`+name+`":[`...)
		for i, recs := range results {
			if i > 0 {
				buf = append(buf, ',')
			}
			if !enabled(payloads[i]) {
				buf = append(buf, "null"...)
				continue
			}
			buf = append(buf, '[')
			for j, r := range recs {
				if j > 0 {
					buf = append(buf, ',')
				}
				buf = each(buf, r)
			}
			buf = append(buf, ']')
		}
		buf = append(buf, ']')
	}
	column("documents", func(p search.Payload) bool { return has(p.Select, search.KeyDocument) },
		func(b []byte, r store.SearchRecord) []byte { return appendStrPtr(b, r.Document) })
	column("embeddings", func(p search.Payload) bool { return has(p.Select, search.KeyEmbedding) },
		func(b []byte, r store.SearchRecord) []byte { return wire.AppendFloat32s(b, r.Embedding) })
	column("metadatas", func(p search.Payload) bool {
		if has(p.Select, search.KeyMetadata) {
			return true
		}
		return len(p.Select) > 0 && searchIsField(p.Select[len(p.Select)-1])
	}, func(b []byte, r store.SearchRecord) []byte { return wire.AppendMetadata(b, r.Metadata) })
	column("scores", func(p search.Payload) bool { return has(p.Select, search.KeyScore) },
		func(b []byte, r store.SearchRecord) []byte {
			if r.Score == nil {
				return append(b, "null"...)
			}
			return append(b, wire.FormatFloat32(*r.Score)...)
		})
	buf = append(buf, `,"select":[`...)
	for i, p := range payloads {
		if i > 0 {
			buf = append(buf, ',')
		}
		sel := p.Select
		if sel == nil {
			sel = []string{}
		}
		b, _ := json.Marshal(sel)
		buf = append(buf, b...)
	}
	buf = append(buf, "]}"...)
	return buf
}

func searchIsField(k string) bool {
	switch k {
	case search.KeyDocument, search.KeyEmbedding, search.KeyMetadata, search.KeyScore:
		return false
	}
	return true
}
