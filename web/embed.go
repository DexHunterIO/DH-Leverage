// Package web embeds the DH-Leverage frontend bundle so the API binary
// ships with its UI. Single-file vanilla JS for now; replace IndexHTML with
// a filesystem when the frontend grows beyond one page.
package web

import _ "embed"

//go:embed index.html
var IndexHTML []byte
