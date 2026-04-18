package shell

import "embed"

// Assets contains the built shell UI served by event-bus-web.
//
//go:embed dist/*
var Assets embed.FS
