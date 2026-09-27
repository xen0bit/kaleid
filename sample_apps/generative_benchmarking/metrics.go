package main

import "math"

// Metrics match pytrec_eval's measures as used by Chroma's generative
// benchmarking toolkit: ndcg_cut, map_cut, recall and P at each k, averaged
// over queries and rounded to 5 decimals.
type Metrics struct {
	NDCG      map[string]float64 `json:"NDCG"`
	MAP       map[string]float64 `json:"MAP"`
	Recall    map[string]float64 `json:"Recall"`
	Precision map[string]float64 `json:"Precision"`
}

// Evaluate scores ranked results against binary relevance judgements.
// ranked[q] is the retrieved document ids for query q, best first;
// relevant[q] is the set of relevant document ids.
func Evaluate(ranked map[string][]string, relevant map[string]map[string]bool, ks []int) Metrics {
	m := Metrics{NDCG: map[string]float64{}, MAP: map[string]float64{}, Recall: map[string]float64{}, Precision: map[string]float64{}}
	n := 0
	for q, rel := range relevant {
		if len(rel) == 0 {
			continue
		}
		n++
		docs := ranked[q]
		for _, k := range ks {
			hits, apSum, dcg := 0, 0.0, 0.0
			for i := 0; i < k && i < len(docs); i++ {
				if rel[docs[i]] {
					hits++
					apSum += float64(hits) / float64(i+1)
					dcg += 1 / math.Log2(float64(i+2))
				}
			}
			idcg := 0.0
			for i := 0; i < min(k, len(rel)); i++ {
				idcg += 1 / math.Log2(float64(i+2))
			}
			m.NDCG[key("NDCG", k)] += dcg / idcg
			m.MAP[key("MAP", k)] += apSum / float64(len(rel))
			m.Recall[key("Recall", k)] += float64(hits) / float64(len(rel))
			m.Precision[key("P", k)] += float64(hits) / float64(k)
		}
	}
	for _, mm := range []map[string]float64{m.NDCG, m.MAP, m.Recall, m.Precision} {
		for k, v := range mm {
			if n > 0 {
				mm[k] = math.Round(v/float64(n)*1e5) / 1e5
			}
		}
	}
	return m
}

func key(name string, k int) string {
	return name + "@" + itoa(k)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
