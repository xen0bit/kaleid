package embed

import (
	"hash/fnv"
	"sort"

	"github.com/xen0bit/kaleid/pkg/client"
)

// BM25 encodes text as sparse vectors for BM25 keyword search.
//
// Documents carry saturated term frequencies,
// tf*(k+1) / (tf + k*(1 - b + b*len/avgLen)); queries carry weight 1 per
// term. The server supplies the other half of BM25: when a collection's
// schema enables a sparse index with "bm25": true, Kaleid (like Chroma
// Cloud) multiplies query weights by each term's inverse document frequency
// at search time. This mirrors the shape of Chroma's BM25 embedding
// function, but tokenization differs (no stemming), so vectors are not
// interchangeable with those produced by Chroma's Python/JS clients.
type BM25 struct {
	K         float64
	B         float64
	AvgDocLen float64
	// Stopwords are dropped before scoring. Defaults to a small English list.
	Stopwords map[string]bool
}

// NewBM25 returns an encoder with Chroma's default parameters.
func NewBM25() *BM25 {
	return &BM25{K: 1.2, B: 0.75, AvgDocLen: 256, Stopwords: EnglishStopwords}
}

func (b *BM25) terms(text string) (map[string]int, int) {
	counts := map[string]int{}
	n := 0
	for _, w := range Tokenize(text) {
		if b.Stopwords[w] {
			continue
		}
		counts[w]++
		n++
	}
	return counts, n
}

// termIndex hashes a term into a 31-bit sparse dimension.
func termIndex(term string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(term))
	return h.Sum32() & 0x7fffffff
}

func build(weights map[uint32]float32, tokens map[uint32]string) client.SparseVector {
	idx := make([]uint32, 0, len(weights))
	for i := range weights {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, c int) bool { return idx[a] < idx[c] })
	sv := client.SparseVector{Indices: idx, Values: make([]float32, len(idx)), Tokens: make([]string, len(idx))}
	for j, i := range idx {
		sv.Values[j] = weights[i]
		sv.Tokens[j] = tokens[i]
	}
	return sv
}

// EncodeDocument returns the sparse vector for a document.
func (b *BM25) EncodeDocument(text string) client.SparseVector {
	counts, n := b.terms(text)
	weights := map[uint32]float32{}
	tokens := map[uint32]string{}
	norm := b.K * (1 - b.B + b.B*float64(n)/b.AvgDocLen)
	for term, tf := range counts {
		i := termIndex(term)
		weights[i] += float32(float64(tf) * (b.K + 1) / (float64(tf) + norm))
		tokens[i] = term
	}
	return build(weights, tokens)
}

// EncodeQuery returns the sparse vector for a query.
func (b *BM25) EncodeQuery(text string) client.SparseVector {
	counts, _ := b.terms(text)
	weights := map[uint32]float32{}
	tokens := map[uint32]string{}
	for term := range counts {
		i := termIndex(term)
		weights[i] = 1
		tokens[i] = term
	}
	return build(weights, tokens)
}

// BM25Schema returns a collection schema that enables a BM25 sparse index on
// key. Pass it as CreateCollectionOptions.Schema.
func BM25Schema(key string) []byte {
	return []byte(`{"defaults":{},"keys":{"` + key + `":{"sparse_vector":{"sparse_vector_index":{"enabled":true,"config":{"bm25":true}}}}}}`)
}

// EnglishStopwords is a small English stopword list.
var EnglishStopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range []string{
		"a", "an", "and", "are", "as", "at", "be", "but", "by", "for", "from", "has", "have", "he",
		"her", "his", "i", "if", "in", "into", "is", "it", "its", "me", "my", "no", "not", "of", "on",
		"or", "our", "she", "so", "such", "than", "that", "the", "their", "them", "then", "there",
		"these", "they", "this", "to", "was", "we", "were", "what", "when", "where", "which", "who",
		"will", "with", "you", "your",
	} {
		m[w] = true
	}
	return m
}()
