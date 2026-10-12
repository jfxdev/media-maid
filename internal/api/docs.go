package api

import (
	_ "embed"
	"net/http"
)

// openAPISpec is the OpenAPI description of this API, served at /openapi.yaml.
//
//go:embed openapi.yaml
var openAPISpec []byte

const swaggerUIVersion = "5.17.14"

// docsPage renders the embedded spec with Swagger UI loaded from a CDN.
const docsPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Media Maid API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui.css">
</head>
<body>
<div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@` + swaggerUIVersion + `/swagger-ui-bundle.js"></script>
<script src="/docs/init.js"></script>
</body>
</html>
`

// docsInit is served as a separate file so the CSP needs no 'unsafe-inline'.
const docsInit = `SwaggerUIBundle({url: "/openapi.yaml", dom_id: "#ui"});
`

const docsCSP = "default-src 'none'; script-src 'self' https://cdn.jsdelivr.net; " +
	"style-src https://cdn.jsdelivr.net 'unsafe-inline'; img-src 'self' data:; connect-src 'self'"

func serveSpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(openAPISpec)
}

func serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", docsCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(docsPage))
}

func serveDocsInit(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(docsInit))
}
