package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// get runs one request against the handler.
func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestHandlerServesTheShell pins the entry point: "/" must answer the embedded
// index, and it must be the page that names the program.
func TestHandlerServesTheShell(t *testing.T) {
	rec := get(t, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", got)
	}
	body := rec.Body.String()
	for _, want := range []string{"OKEGuiDX", "/static/app.js", "/api/v1"} {
		if !strings.Contains(body, want) {
			t.Errorf("index does not mention %q:\n%s", want, body)
		}
	}
}

// TestHandlerServesAssets covers the static tree the shell references.
func TestHandlerServesAssets(t *testing.T) {
	tests := []struct {
		path     string
		wantType string
		wantBody string
	}{
		{path: "/static/app.js", wantType: "javascript", wantBody: `const API = "/api/v1"`},
		{path: "/static/style.css", wantType: "text/css", wantBody: "body"},
	}
	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			rec := get(t, tc.path)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s = %d, want 200", tc.path, rec.Code)
			}
			if got := rec.Header().Get("Content-Type"); !strings.Contains(got, tc.wantType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tc.wantType)
			}
			if !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Errorf("GET %s body does not contain %q", tc.path, tc.wantBody)
			}
		})
	}
}

// TestHandlerFallsBackToTheShell covers the single-page convention: a path the
// embedded tree does not contain is the shell, so a client-side route works
// without a server change.
func TestHandlerFallsBackToTheShell(t *testing.T) {
	for _, path := range []string{"/tasks", "/settings", "/deep/route/that/does/not/exist"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "OKEGuiDX") {
			t.Errorf("GET %s did not answer the shell", path)
		}
	}
}

// TestHandlerRefusesTraversal asserts a hostile path cannot escape the embedded
// tree: the fallback answers the shell rather than reading a file.
func TestHandlerRefusesTraversal(t *testing.T) {
	for _, path := range []string{"/../web.go", "/static/../../web.go", "/%2e%2e/web.go"} {
		rec := get(t, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the shell", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "package web") {
			t.Errorf("GET %s returned the Go source", path)
		}
	}
}

// TestHandlerRejectsNonGet asserts the UI handler only answers reads.
func TestHandlerRejectsNonGet(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST / = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to list GET", allow)
	}
}

// TestHandlerDisablesCaching pins the freshness rule: the UI ships inside the
// binary, so a cached copy is always a stale copy.
func TestHandlerDisablesCaching(t *testing.T) {
	rec := get(t, "/")
	if got := rec.Header().Get("Cache-Control"); got != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache", got)
	}
}

// TestFSExposesTheEmbeddedTree asserts the embedded files are reachable through
// FS, which is what a test or a future build step walks.
func TestFSExposesTheEmbeddedTree(t *testing.T) {
	f, err := FS().Open(indexFile)
	if err != nil {
		t.Fatalf("FS().Open(%q) error = %v", indexFile, err)
	}
	defer f.Close()
	raw, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read embedded index: %v", err)
	}
	if !strings.Contains(string(raw), "OKEGuiDX") {
		t.Error("the embedded index does not name the program")
	}
}

// TestAssetName pins the URL-to-path mapping, including the root case.
func TestAssetName(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "/", want: indexFile},
		{path: "", want: indexFile},
		{path: "/static/app.js", want: "static/app.js"},
		{path: "/static/", want: "static"},
		{path: "/../web.go", want: "web.go"},
	}
	for _, tc := range tests {
		if got := assetName(tc.path); got != tc.want {
			t.Errorf("assetName(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
