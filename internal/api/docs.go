package api

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.json
var openapiSpec []byte

const swaggerHTML = `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>kaleid API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css">
</head>
<body>
<div id="swagger-ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>window.ui = SwaggerUIBundle({url: "/openapi.json", dom_id: "#swagger-ui"});</script>
</body>
</html>`

func (s *Server) openapi(w http.ResponseWriter, r *http.Request) {
	writeRaw(w, http.StatusOK, openapiSpec)
}

func (s *Server) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}
