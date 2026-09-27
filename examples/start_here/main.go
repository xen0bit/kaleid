// Start here: basic embedding retrieval.
//
// A Go port of Chroma's examples/basic_functionality/start_here.ipynb. It
// stores supporting passages for science questions and retrieves the most
// relevant passage for each question.
//
// By default a small built-in sample is used so the example runs offline.
// Pass -sciq N to download N rows of the SciQ dataset from Hugging Face.
//
//	go run ./examples/start_here
//	EMBEDDER=openai OPENAI_API_KEY=... go run ./examples/start_here -sciq 2000
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/xen0bit/kaleid/internal/exampleenv"
	"github.com/xen0bit/kaleid/pkg/client"
)

type sample struct {
	Question string `json:"question"`
	Support  string `json:"support"`
}

// builtin is a small science set written for this example.
var builtin = []sample{
	{"What gas do plants absorb from the air to make food?", "During photosynthesis, plants absorb carbon dioxide from the air and use light energy to turn it into glucose, releasing oxygen as a by-product."},
	{"What is the powerhouse of the cell?", "Mitochondria are organelles that produce most of the cell's supply of ATP, the molecule cells use for energy, which is why they are called the powerhouse of the cell."},
	{"At what temperature does pure water boil at sea level?", "At standard atmospheric pressure at sea level, pure water boils at 100 degrees Celsius, the point where its vapor pressure equals the surrounding air pressure."},
	{"What force keeps the planets in orbit around the sun?", "Gravity is the attractive force between masses; the sun's gravity continuously pulls the planets toward it, bending their paths into orbits."},
	{"What particle in an atom has a negative charge?", "Atoms contain protons with a positive charge, neutrons with no charge, and electrons, which carry a negative charge and occupy regions around the nucleus."},
	{"Which organ pumps blood through the human body?", "The heart is a muscular organ that pumps blood through the circulatory system, delivering oxygen and nutrients to tissues."},
	{"What is the chemical symbol for sodium?", "Sodium is a soft, reactive alkali metal whose chemical symbol, Na, comes from its Latin name natrium."},
	{"What type of rock forms from cooled lava?", "Igneous rock forms when molten rock such as magma or lava cools and solidifies; basalt is a common igneous rock formed from lava."},
	{"What do we call animals that eat only plants?", "Herbivores are animals whose diet consists of plants; examples include deer, rabbits and cows."},
	{"How long does the Earth take to orbit the sun?", "The Earth completes one orbit around the sun in about 365.25 days, which is why a leap day is added every four years."},
	{"What is the process by which a liquid becomes a gas?", "Evaporation is the process in which molecules at the surface of a liquid gain enough energy to escape and become a gas."},
	{"Which blood cells help fight infection?", "White blood cells are part of the immune system and help the body fight infection by attacking bacteria, viruses and other invaders."},
}

// loadSciQ downloads rows of allenai/sciq that have a supporting passage.
func loadSciQ(ctx context.Context, n int) ([]sample, error) {
	var out []sample
	for offset := 0; len(out) < n; offset += 100 {
		url := "https://datasets-server.huggingface.co/rows?dataset=allenai/sciq&config=default&split=train&offset=" +
			strconv.Itoa(offset) + "&length=100"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		var page struct {
			Rows []struct {
				Row sample `json:"row"`
			} `json:"rows"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, err
		}
		if len(page.Rows) == 0 {
			break
		}
		for _, r := range page.Rows {
			if r.Row.Support != "" && len(out) < n {
				out = append(out, r.Row)
			}
		}
	}
	return out, nil
}

func main() {
	sciq := flag.Int("sciq", 0, "download this many SciQ rows from Hugging Face instead of the built-in sample")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	data := builtin
	if *sciq > 0 {
		var err error
		data, err = loadSciQ(ctx, *sciq)
		exampleenv.Must(err)
	}
	fmt.Println("Number of questions with support:", len(data))

	c := exampleenv.Client()
	emb := exampleenv.Embedder()

	// Start from a clean slate so the example can be re-run.
	name := "sciq_supports"
	if err := c.DeleteCollection(ctx, name); err != nil && !client.IsNotFound(err) {
		exampleenv.Must(err)
	}
	col, err := c.CreateCollection(ctx, name, &client.CreateCollectionOptions{
		HNSW: &client.HNSWConfig{Space: client.SpaceCosine},
	})
	exampleenv.Must(err)

	// Embed and load the supporting evidence in batches of 1000.
	const batch = 1000
	for i := 0; i < len(data); i += batch {
		end := min(i+batch, len(data))
		var ids, docs []string
		var mds []client.Metadata
		for j := i; j < end; j++ {
			ids = append(ids, strconv.Itoa(j))
			docs = append(docs, data[j].Support)
			mds = append(mds, client.Metadata{"type": "support"})
		}
		vecs, err := emb.Embed(ctx, docs)
		exampleenv.Must(err)
		exampleenv.Must(col.Add(ctx, client.Records{IDs: ids, Embeddings: vecs, Documents: docs, Metadatas: mds}))
	}

	// Query with the first ten questions and print the best supporting passage.
	n := min(10, len(data))
	questions := make([]string, n)
	for i := range questions {
		questions[i] = data[i].Question
	}
	qvecs, err := emb.Embed(ctx, questions)
	exampleenv.Must(err)
	res, err := col.Query(ctx, client.QueryOptions{Embeddings: qvecs, NResults: 1})
	exampleenv.Must(err)

	correct := 0
	for i, q := range questions {
		fmt.Printf("Question: %s\nRetrieved support: %s\n\n", q, *res.Documents[i][0])
		if res.IDs[i][0] == strconv.Itoa(i) {
			correct++
		}
	}
	fmt.Printf("Retrieved the matching support for %d of %d questions (embedder: %s)\n", correct, n, emb.Name())

	// Collections can also be fetched by id, as in Chroma's
	// test_get_collection_by_id notebook.
	same, err := c.GetCollectionByID(ctx, col.ID)
	exampleenv.Must(err)
	count, err := same.Count(ctx)
	exampleenv.Must(err)
	fmt.Printf("Collection %q (%s) holds %d records\n", same.Name, same.ID, count)
}
