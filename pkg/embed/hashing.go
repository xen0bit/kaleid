package embed

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// Hashing is an offline, deterministic embedder based on feature hashing of
// word unigrams, word bigrams and character trigrams (stopwords removed),
// with sublinear term weighting and L2 normalization. Texts that share words and word fragments
// land close together, so it supports lexical-similarity demos and tests
// without a model or network access. It does not understand meaning; use a
// real model (OpenAICompatible) for semantic search.
type Hashing struct {
	Dim int
}

// NewHashing returns a hashing embedder of the given dimension.
func NewHashing(dim int) *Hashing { return &Hashing{Dim: dim} }

// Name implements Embedder.
func (h *Hashing) Name() string { return fmt.Sprintf("hash-%d", h.Dim) }

// Embed implements Embedder.
func (h *Hashing) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = h.embedOne(t)
	}
	return out, nil
}

func (h *Hashing) add(v []float64, feature string, weight float64) {
	f := fnv.New64a()
	f.Write([]byte(feature))
	sum := f.Sum64()
	idx := int(sum % uint64(h.Dim))
	// The sign bit reduces collision bias (signed feature hashing).
	if sum>>63 == 1 {
		weight = -weight
	}
	v[idx] += weight
}

func (h *Hashing) embedOne(text string) []float32 {
	v := make([]float64, h.Dim)
	words := contentWords(Tokenize(text))
	counts := map[string]int{}
	for _, w := range words {
		counts["w:"+w]++
		padded := "^" + w + "$"
		r := []rune(padded)
		for j := 0; j+3 <= len(r); j++ {
			counts["c:"+string(r[j:j+3])]++
		}
	}
	for j := 0; j+1 < len(words); j++ {
		counts["b:"+words[j]+" "+words[j+1]]++
	}
	for feature, n := range counts {
		weight := 1 + math.Log(float64(n))
		switch feature[0] {
		case 'c':
			weight *= 0.5
		case 'b':
			weight *= 0.75
		}
		h.add(v, feature, weight)
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	norm = math.Sqrt(norm)
	out := make([]float32, h.Dim)
	if norm == 0 {
		// An all-zero vector has no direction; give empty text a fixed one.
		out[0] = 1
		return out
	}
	for i, x := range v {
		out[i] = float32(x / norm)
	}
	return out
}

// contentWords drops stopwords unless nothing else remains.
func contentWords(words []string) []string {
	out := make([]string, 0, len(words))
	for _, w := range words {
		if !EnglishStopwords[w] {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		return words
	}
	return out
}

// Tokenize lowercases text and splits it into alphanumeric words.
func Tokenize(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}
