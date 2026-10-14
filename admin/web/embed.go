// Package web embeds the admin page.
package web

import "embed"

//go:embed index.html app.js style.css
var Files embed.FS
