// Package webui exposes the existing frontend as an embedded HTTP filesystem.
package webui

import "embed"

// Assets contains the browser UI used by the headless Linux server.
//
//go:embed index.html login.html main.js style.css web-api.js
var Assets embed.FS
