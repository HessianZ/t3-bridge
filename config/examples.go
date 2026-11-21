package config

import "embed"

//go:embed *.example.json
var Examples embed.FS
