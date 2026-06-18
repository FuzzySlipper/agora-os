package shell

import "embed"

// Assets contains the built shell UI served by event-bus-web.
//
//go:generate npm run build
//go:embed dist/* dist/desktop/* dist/desktop/widgets/* dist/shared/*
var Assets embed.FS
