// Package web embeds the browser UI.
package web

import "embed"

//go:embed index.html style.css app.js tree.js
var FS embed.FS
