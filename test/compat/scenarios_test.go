package compat

import "testing"

func TestSystemEndpoints(t *testing.T) {
	run(t, []step{
		{method: "GET", path: "/api/v2/version"},
		{method: "GET", path: "/api/v2/pre-flight-checks"},
		{method: "GET", path: "/api/v2/auth/identity"},
		{method: "GET", path: "/api/v2/healthcheck"},
		{method: "GET", path: "/api/v2/heartbeat", ignore: []string{"nanosecond heartbeat"}},
		{method: "GET", path: "/api/v1/heartbeat"},
		{method: "GET", path: "/api/v2/does-not-exist"},
	})
}

func TestTenantsAndDatabases(t *testing.T) {
	run(t, []step{
		{method: "POST", path: "/api/v2/tenants", body: `{"name":"t{sfx}"}`},
		{method: "POST", path: "/api/v2/tenants", body: `{"name":"t{sfx}"}`},
		// Chroma prints a Rust HashMap here, so key order in the message varies.
		{method: "POST", path: "/api/v2/tenants", body: `{"name":"ab"}`, ignore: []string{"message"}},
		{method: "GET", path: "/api/v2/tenants/t{sfx}"},
		{method: "GET", path: "/api/v2/tenants/missing{sfx}"},
		{method: "POST", path: "/api/v2/tenants/t{sfx}/databases", body: `{"name":"db1"}`},
		{method: "POST", path: "/api/v2/tenants/t{sfx}/databases", body: `{"name":"db1"}`},
		{method: "POST", path: "/api/v2/tenants/t{sfx}/databases", body: `{"name":"db0"}`},
		{method: "POST", path: "/api/v2/tenants/t{sfx}/databases", body: `{"name":"x"}`},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases", ignore: nil, unordered: false},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases?limit=1&offset=1"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/db1"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/nope"},
		{method: "DELETE", path: "/api/v2/tenants/t{sfx}/databases/nope"},
		{method: "DELETE", path: "/api/v2/tenants/t{sfx}/databases/db1"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/ab/collections"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/nodb/collections"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/nodb/collections_count"},
	})
}

func TestCollectionLifecycle(t *testing.T) {
	c := "/api/v2/tenants/t{sfx}/databases/d{sfx}/collections"
	run(t, []step{
		{method: "POST", path: "/api/v2/tenants", body: `{"name":"t{sfx}"}`},
		{method: "POST", path: "/api/v2/tenants/t{sfx}/databases", body: `{"name":"d{sfx}"}`},
		{method: "POST", path: c, body: `{"name":"plain"}`, capture: "plain"},
		{method: "POST", path: c, body: `{"name":"plain"}`},
		{method: "POST", path: c, body: `{"name":"plain","get_or_create":true,"metadata":{"x":1}}`},
		{method: "POST", path: c, body: `{"name":"p"}`},
		{method: "POST", path: c, body: `{"name":"a..b"}`},
		{method: "POST", path: c, body: `{"name":"10.0.0.1"}`},
		{method: "POST", path: c, body: `{"name":"-bad"}`},
		{method: "POST", path: c, body: `{"name":"legacy","metadata":{"hnsw:space":"ip","hnsw:M":32,"hnsw:search_ef":20}}`},
		{method: "POST", path: c, body: `{"name":"legacybad","metadata":{"hnsw:bogus":1}}`},
		{method: "POST", path: c, body: `{"name":"cfgip","configuration":{"hnsw":{"space":"ip","ef_search":50}}}`, capture: "cfgip"},
		{method: "POST", path: c, body: `{"name":"cfgspann","configuration":{"spann":{"space":"cosine"}}}`},
		{method: "POST", path: c, body: `{"name":"cfgboth","configuration":{"hnsw":{},"spann":{}}}`},
		{method: "POST", path: c, body: `{"name":"cfgunknown","configuration":{"hnsw":{"bogus":1}}}`, ignore: []string{"message"}},
		{method: "POST", path: c, body: `{"name":"cfgprec","configuration":{"hnsw":{"space":"cosine"}},"metadata":{"hnsw:space":"ip"}}`},
		{method: "POST", path: c, body: `{"name":"withef","configuration":{"embedding_function":{"type":"known","name":"openai","config":{"model_name":"text-embedding-3-small","api_key_env_var":"OPENAI_API_KEY"}}}}`},
		{method: "POST", path: c, body: `{"name":"emptymd","metadata":{}}`},
		{method: "POST", path: c, body: `{"name":"nullmd","metadata":{"n":null}}`, ignore: []string{"message"}},
		{method: "POST", path: c, body: `{"name":"badkey","metadata":{"#x":1}}`, ignore: []string{"message"}},
		{method: "GET", path: c + "/plain"},
		{method: "GET", path: c + "/nope"},
		{method: "GET", path: c + "/by-id/{plain}"},
		{method: "GET", path: c + "/by-id/00000000-0000-0000-0000-00000000abcd"},
		{method: "GET", path: c + "/{plain}/count"},
		{method: "GET", path: c + "/notauuid/count"},
		{method: "GET", path: c + "/00000000-0000-0000-0000-00000000abcd/count"},
		{method: "PUT", path: c + "/plain", body: `{}`},
		{method: "PUT", path: c + "/{cfgip}", body: `{"new_configuration":{"hnsw":{"ef_search":77}}}`},
		{method: "PUT", path: c + "/{cfgip}", body: `{"new_configuration":{"hnsw":{"space":"l2"}}}`, ignore: []string{"message"}},
		{method: "PUT", path: c + "/{cfgip}", body: `{"new_metadata":{}}`},
		{method: "PUT", path: c + "/{cfgip}", body: `{"new_name":"renamed","new_metadata":{"a":1,"f":2.0}}`},
		{method: "GET", path: c + "/renamed"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/d{sfx}/collections_count"},
		{method: "GET", path: c + "?limit=0"},
		{method: "GET", path: c + "?limit=-1"},
		{method: "DELETE", path: c + "/renamed"},
		{method: "DELETE", path: c + "/renamed"},
		{method: "GET", path: "/api/v2/tenants/t{sfx}/databases/d{sfx}/collections_count"},
	})
}

func TestRecordsSemantics(t *testing.T) {
	c := coll + "/{col}"
	run(t, []step{
		{method: "POST", path: coll, body: `{"name":"rec{sfx}","metadata":{"hnsw:space":"cosine"}}`, capture: "col"},
		{method: "POST", path: c + "/add", body: `{"ids":["a","b","c"],"embeddings":[[1,0,0],[0,1,0],[0.6,0.8,0]],"documents":["hello world","Foo bar",null],"metadatas":[{"k":1,"tags":["x","y"],"f":3.0},{"k":2.5,"s":"str","flag":true},null]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["a"],"embeddings":[[0,0,1]],"documents":["replaced?"]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["d","d"],"embeddings":[[1,0,0],[0,0,1]]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["e"],"embeddings":[[1,0]]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["e","f"],"embeddings":[[1,0,0]]}`},
		{method: "POST", path: c + "/add", body: `{"ids":[""],"embeddings":[[1,0,0]]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["e"],"embeddings":[[1,0,0]],"metadatas":[{"#bad":1}]}`},
		{method: "POST", path: c + "/add", body: `{"ids":["e"],"embeddings":null}`, ignore: []string{"message"}},
		{method: "POST", path: c + "/add", body: `{"ids":["b64"],"embeddings":["AACAPgAAQD8AAAAA"]}`},
		{method: "POST", path: c + "/update", body: `{"ids":["zzz"],"documents":["x"]}`},
		{method: "POST", path: c + "/update", body: `{"ids":["a"],"metadatas":[{"k":10,"f":null,"z":"new"}]}`},
		{method: "POST", path: c + "/update", body: `{"ids":["c"],"documents":[null],"metadatas":[{"only":1}]}`},
		{method: "POST", path: c + "/upsert", body: `{"ids":["b","n1"],"embeddings":[[0,2,0],[0,0,3]],"metadatas":[{"w":1},{"new":true}],"documents":[null,"new doc"]}`},
		{method: "GET", path: c + "/count"},
		{method: "POST", path: c + "/get", body: `{"include":["documents","metadatas","embeddings","uris"]}`},
		{method: "POST", path: c + "/get", body: `{"ids":["c","a","zz"],"include":[]}`},
		{method: "POST", path: c + "/get", body: `{"limit":2,"offset":1}`},
		{method: "POST", path: c + "/get", body: `{"include":["bogus"]}`, ignore: []string{"message"}},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0,0],[0,1,0]],"n_results":3,"include":["distances","documents","metadatas","embeddings"]}`},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0,0]],"n_results":10}`},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0,0]],"n_results":0}`},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0,0]],"n_results":2,"ids":["b","c"]}`},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0,0]],"where":{"k":{"$gte":2}}}`},
		{method: "POST", path: c + "/query", body: `{"query_embeddings":[[1,0]]}`},
		{method: "POST", path: c + "/delete", body: `{"where":{"k":2.5}}`},
		{method: "POST", path: c + "/delete", body: `{}`},
		{method: "POST", path: c + "/delete", body: `{"ids":["n1","nope"]}`},
		{method: "POST", path: c + "/get", body: `{}`},
	})
}

func TestDistanceSpaces(t *testing.T) {
	for _, space := range []string{"l2", "ip", "cosine"} {
		c := coll + "/{col}"
		run(t, []step{
			{method: "POST", path: coll, body: `{"name":"sp` + space + `{sfx}","configuration":{"hnsw":{"space":"` + space + `"}}}`, capture: "col"},
			{method: "POST", path: c + "/add", body: `{"ids":["u","v","w"],"embeddings":[[2,0],[0.5,0.5],[-1,3]]}`},
			{method: "POST", path: c + "/query", body: `{"query_embeddings":[[3,0],[0.1,0.9]],"n_results":3,"include":["distances"]}`},
		})
	}
}

func TestFilters(t *testing.T) {
	c := coll + "/{col}"
	get := func(where string) step {
		return step{method: "POST", path: c + "/get", body: `{"include":[],` + where + `}`}
	}
	run(t, []step{
		{method: "POST", path: coll, body: `{"name":"flt{sfx}"}`, capture: "col"},
		{method: "POST", path: c + "/add", body: `{"ids":["a","b","c","d","e"],"embeddings":[[1],[2],[3],[4],[5]],
			"documents":["The quick brown fox","lazy dog 50% off","Under_score and API docs",null,"line one\nline two"],
			"metadatas":[{"i":1,"f":1.5,"s":"x","b":true,"arr":["p","q"],"nums":[1,2]},{"i":2,"f":2.0,"s":"y","b":false,"arr":["q"]},{"i":3,"s":"x","nums":[3.5]},null,{"i":-1,"f":0.0,"b":true}]}`},
		get(`"where":{"i":1}`),
		get(`"where":{"i":{"$ne":1}}`),
		get(`"where":{"missing":{"$ne":1}}`),
		get(`"where":{"missing":{"$eq":1}}`),
		get(`"where":{"f":2}`),
		get(`"where":{"i":2.0}`),
		get(`"where":{"i":{"$gt":1}}`),
		get(`"where":{"i":{"$gte":2}}`),
		get(`"where":{"f":{"$lt":2}}`),
		get(`"where":{"f":{"$lte":2}}`),
		get(`"where":{"i":{"$in":[1,3]}}`),
		get(`"where":{"i":{"$in":[1.0,3]}}`),
		get(`"where":{"i":{"$nin":[1,3]}}`),
		get(`"where":{"s":{"$in":["x"]}}`),
		get(`"where":{"s":{"$nin":["x"]}}`),
		get(`"where":{"b":true}`),
		get(`"where":{"b":{"$ne":true}}`),
		get(`"where":{"b":1}`),
		get(`"where":{"b":{"$in":[false]}}`),
		get(`"where":{"arr":{"$contains":"q"}}`),
		get(`"where":{"arr":{"$not_contains":"p"}}`),
		get(`"where":{"nums":{"$contains":1}}`),
		get(`"where":{"s":{"$contains":"x"}}`),
		get(`"where":{"arr":"q"}`),
		get(`"where":{"$and":[{"s":"x"},{"i":{"$gt":1}}]}`),
		get(`"where":{"$or":[{"s":"y"},{"i":{"$lt":0}}]}`),
		get(`"where":{"$and":[]}`),
		get(`"where":{"$or":[]}`),
		get(`"where":{"#document":{"$contains":"lazy"}}`),
		get(`"where_document":{"$contains":"quick"}`),
		get(`"where_document":{"$contains":"QUICK"}`),
		get(`"where_document":{"$contains":"50%"}`),
		get(`"where_document":{"$contains":"_"}`),
		get(`"where_document":{"$not_contains":"dog"}`),
		get(`"where_document":{"$regex":"^The"}`),
		get(`"where_document":{"$regex":"\\bAPI\\b"}`),
		get(`"where_document":{"$regex":"(?i)QUICK"}`),
		get(`"where_document":{"$regex":"[0-9]+%"}`),
		get(`"where_document":{"$regex":"(?m)^line two$"}`),
		get(`"where_document":{"$not_regex":"o"}`),
		get(`"where_document":{"$and":[{"$contains":"o"},{"$not_contains":"quick"}]}`),
		get(`"where_document":{"$or":[{"$contains":"fox"},{"$regex":"docs$"}]}`),
		get(`"where":{"s":"x"},"where_document":{"$contains":"API"}`),
		get(`"where":{}`),
		get(`"where":{"a":1,"b":2}`),
		get(`"where":{"i":{"$gt":"x"}}`),
		get(`"where":{"i":{"$in":[]}}`),
		get(`"where":{"i":{"$in":[1,"x"]}}`),
		get(`"where":{"s":{"$regex":"x"}}`),
		get(`"where_document":{"$eq":"x"}`),
		{method: "POST", path: c + "/get", body: `{"include":[],"where_document":{"$regex":"("}}`, ignore: []string{"message"}},
		{method: "POST", path: c + "/delete", body: `{"where":{"i":{"$gte":2}},"limit":1}`},
		get(`"where":{"i":{"$gte":2}}`),
	})
}

func TestUpdateAndUpsertMerging(t *testing.T) {
	c := coll + "/{col}"
	run(t, []step{
		{method: "POST", path: coll, body: `{"name":"merge{sfx}"}`, capture: "col"},
		{method: "POST", path: c + "/add", body: `{"ids":["a","b"],"embeddings":[[1,0],[0,1]],"metadatas":[{"x":1,"y":"keep"},{"only":1}],"uris":["u1",null]}`},
		{method: "POST", path: c + "/upsert", body: `{"ids":["a","a","z"],"embeddings":[[1,1],[2,2],[3,3]],"metadatas":[{"x":2},{"y":null},{"z":1}]}`},
		{method: "POST", path: c + "/update", body: `{"ids":["b"],"metadatas":[{"only":null}]}`},
		{method: "POST", path: c + "/update", body: `{"ids":["a"],"embeddings":[null],"uris":["u2"]}`},
		{method: "POST", path: c + "/get", body: `{"include":["metadatas","embeddings","uris","documents"]}`},
	})
}
