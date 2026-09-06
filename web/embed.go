package web

import "embed"

// Assets contains the self-contained dashboard. No CDN or external font calls are used.
//
//go:embed index.html app.css app.js
var Assets embed.FS
