// Copyright 2026 Zyvor AI Labs · https://zyvor.dev
// SPDX-License-Identifier: Apache-2.0

package web

import "embed"

// Assets contains the self-contained dashboard. No CDN or external font calls are used.
//
//go:embed index.html app.css app.js login.css zyvor-mark.svg
var Assets embed.FS
