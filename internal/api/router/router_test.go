package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func TestFrontendRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	frontend := fstest.MapFS{
		"index.html":      &fstest.MapFile{Data: []byte("<!doctype html><div id=\"app\"></div>")},
		"assets/index.js": &fstest.MapFile{Data: []byte("console.log('ok')")},
	}
	r := gin.New()
	InitRouterWithFrontend(r, "test-key", frontend)

	tests := []struct {
		name         string
		path         string
		wantStatus   int
		wantContains string
		wantNoHTML   bool
	}{
		{
			name:         "root returns index",
			path:         "/",
			wantStatus:   http.StatusOK,
			wantContains: "<!doctype html>",
		},
		{
			name:         "existing asset returns file",
			path:         "/assets/index.js",
			wantStatus:   http.StatusOK,
			wantContains: "console.log('ok')",
		},
		{
			name:         "spa path falls back to index",
			path:         "/dashboard",
			wantStatus:   http.StatusOK,
			wantContains: "<!doctype html>",
		},
		{
			name:       "missing asset returns not found",
			path:       "/assets/missing.js",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "api path does not fall back to index",
			path:       "/api/missing",
			wantStatus: http.StatusNotFound,
			wantNoHTML: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := performRouterRequest(r, http.MethodGet, tt.path)
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.wantContains != "" && !strings.Contains(w.Body.String(), tt.wantContains) {
				t.Fatalf("body %q does not contain %q", w.Body.String(), tt.wantContains)
			}
			if tt.wantNoHTML && strings.Contains(w.Body.String(), "<!doctype html>") {
				t.Fatalf("api response fell back to frontend html: %s", w.Body.String())
			}
		})
	}
}

func TestFrontendRoutesDisabledWithoutFS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	r := gin.New()
	InitRouterWithFrontend(r, "test-key", nil)

	w := performRouterRequest(r, http.MethodGet, "/")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
}

func performRouterRequest(r http.Handler, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}
