package ui

import "embed"

// StaticFS holds the embedded static assets (CSS, JS).
//
//go:embed static/*
var StaticFS embed.FS
