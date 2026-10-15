// Package web serves the demo page: register, KYC, admin review and live prices.
package web

import (
	_ "embed"
	"net/http"
)

//go:embed index.html
var page []byte // index.html is built into the binary, so there is nothing extra to deploy

// Handler serves the demo page at GET /.
func Handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(page)
}
