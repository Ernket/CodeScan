//go:build embedded_frontend

package main

import (
	"embed"
	"io/fs"
)

//go:embed frontend/dist
var frontendDist embed.FS

func frontendFS() fs.FS {
	dist, err := fs.Sub(frontendDist, "frontend/dist")
	if err != nil {
		panic(err)
	}
	return dist
}
