package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/xen0bit/kaleid/internal/apierr"
	"github.com/xen0bit/kaleid/internal/filter"
	"github.com/xen0bit/kaleid/internal/store"
	"github.com/xen0bit/kaleid/internal/validate"
	"github.com/xen0bit/kaleid/internal/wire"
)

type recordPayload struct {
	IDs        *[]string       `json:"ids"`
	Embeddings json.RawMessage `json:"embeddings"`
	Documents  *[]*string      `json:"documents"`
	URIs       *[]*string      `json:"uris"`
	Metadatas  json.RawMessage `json:"metadatas"`
}

// parseRecordMetadatas parses a list of (null | metadata object).
func parseRecordMetadatas(raw json.RawMessage, allowNullValues bool) ([]wire.UpdateMetadata, []bool, error) {
	if isNullRaw(raw) {
		return nil, nil, nil
	}
	var elems []json.RawMessage
	if err := json.Unmarshal(raw, &elems); err != nil {
		return nil, nil, apierr.Unprocessable("metadatas: invalid type: expected a sequence")
	}
	out := make([]wire.UpdateMetadata, len(elems))
	present := make([]bool, len(elems))
	for i, e := range elems {
		if isNullRaw(e) {
			continue
		}
		md, err := wire.ParseUpdateMetadata(e)
		if err != nil {
			return nil, nil, apierr.Unprocessable("metadatas[%d].%s", i, err.Error())
		}
		if !allowNullValues {
			for k, v := range md {
				if v == nil {
					return nil, nil, apierr.Unprocessable("metadatas[%d].%s: data did not match any variant of untagged enum MetadataValue", i, k)
				}
			}
		}
		out[i] = md
		present[i] = true
	}
	return out, present, nil
}

func (s *Server) parseWrite(r *http.Request, mode store.WriteMode) (store.WriteBatch, error) {
	var p recordPayload
	var b store.WriteBatch
	if err := s.decodeBody(r, &p); err != nil {
		return b, err
	}
	if p.IDs == nil {
		return b, apierr.Unprocessable("missing field `ids`")
	}
	b.IDs = *p.IDs
	requireEmb := mode != store.ModeUpdate
	if isNullRaw(p.Embeddings) {
		if requireEmb {
			if len(bytes.TrimSpace(p.Embeddings)) == 0 {
				return b, apierr.Unprocessable("missing field `embeddings`")
			}
			return b, apierr.Unprocessable("embeddings: data did not match any variant of untagged enum EmbeddingsPayload")
		}
	} else {
		embs, err := wire.ParseEmbeddings(p.Embeddings, !requireEmb)
		if err != nil {
			return b, apierr.Unprocessable("embeddings: %s", err.Error())
		}
		b.Embeddings = embs
	}
	if p.Documents != nil {
		b.Documents = *p.Documents
	}
	if p.URIs != nil {
		b.URIs = *p.URIs
	}
	mds, present, err := parseRecordMetadatas(p.Metadatas, mode != store.ModeAdd)
	if err != nil {
		return b, err
	}
	b.Metadatas, b.MetadataPresent = mds, present

	n := len(b.IDs)
	if (b.Embeddings != nil && len(b.Embeddings) != n) || (b.Documents != nil && len(b.Documents) != n) ||
		(b.URIs != nil && len(b.URIs) != n) || (b.Metadatas != nil && len(b.Metadatas) != n) {
		return b, apierr.InvalidArgument("Inconsistent number of IDs, embeddings, documents, URIs and metadatas")
	}
	for _, id := range b.IDs {
		if id == "" {
			return b, apierr.InvalidArgument("Empty ID, ID must have at least one character")
		}
	}
	if err := validate.RecordMetadatas(b.Metadatas); err != nil {
		return b, err
	}
	return b, nil
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, mode store.WriteMode) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	b, err := s.parseWrite(r, mode)
	if err != nil {
		return err
	}
	if err := s.store.Write(r.Context(), c, mode, b); err != nil {
		return err
	}
	status := http.StatusOK
	if mode == store.ModeAdd {
		status = http.StatusCreated
	}
	writeRaw(w, status, []byte("{}"))
	return nil
}

func (s *Server) add(w http.ResponseWriter, r *http.Request) error {
	return s.write(w, r, store.ModeAdd)
}

func (s *Server) upsert(w http.ResponseWriter, r *http.Request) error {
	return s.write(w, r, store.ModeUpsert)
}

func (s *Server) update(w http.ResponseWriter, r *http.Request) error {
	return s.write(w, r, store.ModeUpdate)
}

func (s *Server) deleteRecords(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	var p struct {
		IDs           *[]string       `json:"ids"`
		Where         json.RawMessage `json:"where"`
		WhereDocument json.RawMessage `json:"where_document"`
		Limit         *uint32         `json:"limit"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	where, err := filter.Parse(p.Where, p.WhereDocument)
	if err != nil {
		return err
	}
	if err := checkFilterIndexes(c.Schema, where); err != nil {
		return err
	}
	var ids []string
	if p.IDs != nil {
		ids = *p.IDs
	}
	var limit *int
	if p.Limit != nil {
		l := int(*p.Limit)
		limit = &l
	}
	n, err := s.store.Delete(r.Context(), c, ids, where, limit)
	if err != nil {
		return err
	}
	writeRaw(w, http.StatusOK, []byte(`{"deleted":`+strconv.Itoa(n)+`}`))
	return nil
}

func (s *Server) count(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	n, err := s.store.Count(r.Context(), c)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, n)
	return nil
}

// parseInclude validates an include list, returning the echoed list.
func parseInclude(raw json.RawMessage, def []string) ([]string, store.Include, error) {
	var inc store.Include
	list := def
	if !isNullRaw(raw) {
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, inc, apierr.Unprocessable("include: invalid type: expected a sequence")
		}
	}
	for _, v := range list {
		switch v {
		case "embeddings":
			inc.Embeddings = true
		case "documents":
			inc.Documents = true
		case "metadatas":
			inc.Metadatas = true
		case "uris":
			inc.URIs = true
		case "distances":
			inc.Distances = true
		default:
			return nil, inc, apierr.Unprocessable("include: unknown variant `%s`, expected one of `distances`, `documents`, `embeddings`, `metadatas`, `uris`", v)
		}
	}
	if list == nil {
		list = []string{}
	}
	return list, inc, nil
}

func appendStrPtr(buf []byte, s *string) []byte {
	if s == nil {
		return append(buf, "null"...)
	}
	b, _ := json.Marshal(*s)
	return append(buf, b...)
}

func appendIncludeList(buf []byte, list []string) []byte {
	b, _ := json.Marshal(list)
	return append(buf, b...)
}

// appendRecordColumns writes the ids/embeddings/documents/uris/metadatas
// columns for a flat record list.
func appendRecordColumns(buf []byte, recs []store.Record, inc store.Include) []byte {
	buf = append(buf, `"ids":[`...)
	for i, r := range recs {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = appendStrPtr(buf, &r.ID)
	}
	buf = append(buf, `],"embeddings":`...)
	if inc.Embeddings {
		buf = append(buf, '[')
		for i, r := range recs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = wire.AppendFloat32s(buf, r.Embedding)
		}
		buf = append(buf, ']')
	} else {
		buf = append(buf, "null"...)
	}
	buf = append(buf, `,"documents":`...)
	if inc.Documents {
		buf = append(buf, '[')
		for i, r := range recs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendStrPtr(buf, r.Document)
		}
		buf = append(buf, ']')
	} else {
		buf = append(buf, "null"...)
	}
	buf = append(buf, `,"uris":`...)
	if inc.URIs {
		buf = append(buf, '[')
		for i, r := range recs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = appendStrPtr(buf, r.URI)
		}
		buf = append(buf, ']')
	} else {
		buf = append(buf, "null"...)
	}
	buf = append(buf, `,"metadatas":`...)
	if inc.Metadatas {
		buf = append(buf, '[')
		for i, r := range recs {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = wire.AppendMetadata(buf, r.Metadata)
		}
		buf = append(buf, ']')
	} else {
		buf = append(buf, "null"...)
	}
	return buf
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	var p struct {
		IDs           *[]string       `json:"ids"`
		Where         json.RawMessage `json:"where"`
		WhereDocument json.RawMessage `json:"where_document"`
		Limit         *uint32         `json:"limit"`
		Offset        *uint32         `json:"offset"`
		Include       json.RawMessage `json:"include"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	list, inc, err := parseInclude(p.Include, []string{"documents", "metadatas"})
	if err != nil {
		return err
	}
	where, err := filter.Parse(p.Where, p.WhereDocument)
	if err != nil {
		return err
	}
	if err := checkFilterIndexes(c.Schema, where); err != nil {
		return err
	}
	var ids []string
	if p.IDs != nil {
		ids = *p.IDs
	}
	var limit *int
	if p.Limit != nil {
		l := int(*p.Limit)
		limit = &l
	}
	offset := 0
	if p.Offset != nil {
		offset = int(*p.Offset)
	}
	recs, err := s.store.Get(r.Context(), c, ids, where, limit, offset, inc)
	if err != nil {
		return err
	}
	buf := append(make([]byte, 0, 256), '{')
	buf = appendRecordColumns(buf, recs, inc)
	buf = append(buf, `,"include":`...)
	buf = appendIncludeList(buf, list)
	buf = append(buf, '}')
	writeRaw(w, http.StatusOK, buf)
	return nil
}

func (s *Server) query(w http.ResponseWriter, r *http.Request) error {
	c, err := s.loadCollection(r)
	if err != nil {
		return err
	}
	var p struct {
		IDs             *[]string       `json:"ids"`
		Where           json.RawMessage `json:"where"`
		WhereDocument   json.RawMessage `json:"where_document"`
		QueryEmbeddings json.RawMessage `json:"query_embeddings"`
		NResults        *uint32         `json:"n_results"`
		Include         json.RawMessage `json:"include"`
	}
	if err := s.decodeBody(r, &p); err != nil {
		return err
	}
	if isNullRaw(p.QueryEmbeddings) {
		return apierr.Unprocessable("missing field `query_embeddings`")
	}
	queries, err := wire.ParseEmbeddings(p.QueryEmbeddings, false)
	if err != nil {
		return apierr.Unprocessable("query_embeddings: %s", err.Error())
	}
	list, inc, err := parseInclude(p.Include, []string{"documents", "metadatas", "distances"})
	if err != nil {
		return err
	}
	where, err := filter.Parse(p.Where, p.WhereDocument)
	if err != nil {
		return err
	}
	if err := checkFilterIndexes(c.Schema, where); err != nil {
		return err
	}
	n := 10
	if p.NResults != nil {
		n = int(*p.NResults)
	}
	var ids []string
	if p.IDs != nil {
		ids = *p.IDs
	}
	results, err := s.store.Query(r.Context(), c, queries, n, ids, where, inc)
	if err != nil {
		return err
	}
	buf := append(make([]byte, 0, 256), `{"ids":[`...)
	for i, recs := range results {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '[')
		for j, rec := range recs {
			if j > 0 {
				buf = append(buf, ',')
			}
			buf = appendStrPtr(buf, &rec.ID)
		}
		buf = append(buf, ']')
	}
	buf = append(buf, ']')
	nested := func(name string, enabled bool, each func([]byte, store.Record) []byte) {
		buf = append(buf, `,"`+name+`":`...)
		if !enabled {
			buf = append(buf, "null"...)
			return
		}
		buf = append(buf, '[')
		for i, recs := range results {
			if i > 0 {
				buf = append(buf, ',')
			}
			buf = append(buf, '[')
			for j, rec := range recs {
				if j > 0 {
					buf = append(buf, ',')
				}
				buf = each(buf, rec)
			}
			buf = append(buf, ']')
		}
		buf = append(buf, ']')
	}
	nested("embeddings", inc.Embeddings, func(b []byte, r store.Record) []byte { return wire.AppendFloat32s(b, r.Embedding) })
	nested("documents", inc.Documents, func(b []byte, r store.Record) []byte { return appendStrPtr(b, r.Document) })
	nested("uris", inc.URIs, func(b []byte, r store.Record) []byte { return appendStrPtr(b, r.URI) })
	nested("metadatas", inc.Metadatas, func(b []byte, r store.Record) []byte { return wire.AppendMetadata(b, r.Metadata) })
	nested("distances", inc.Distances, func(b []byte, r store.Record) []byte { return append(b, wire.FormatFloat32(r.Distance)...) })
	buf = append(buf, `,"include":`...)
	buf = appendIncludeList(buf, list)
	buf = append(buf, '}')
	writeRaw(w, http.StatusOK, buf)
	return nil
}
