//go:build embedded_frontend

package main

import (
	"io/fs"
	"testing"
)

func TestEmbeddedFrontendFS(t *testing.T) {
	frontend := frontendFS()
	if frontend == nil {
		t.Fatal("frontendFS returned nil")
	}
	if _, err := fs.ReadFile(frontend, "index.html"); err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	jsAssets, err := fs.Glob(frontend, "assets/*.js")
	if err != nil {
		t.Fatalf("glob embedded js assets: %v", err)
	}
	if len(jsAssets) == 0 {
		t.Fatal("expected at least one embedded JavaScript asset")
	}
}
