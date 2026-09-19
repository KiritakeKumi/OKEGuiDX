// Package web serves the embedded Web UI.
//
// The front end itself is the F workstream's deliverable. What lives here is
// the embed skeleton plus a minimal page that can list tasks and follow the
// progress stream, so the daemon has something real to serve at "/" while the
// full interface is being built.
//
// The package owns the bytes and the HTTP plumbing, nothing else. Routing
// precedence belongs to the caller (cmd/okegui), which registers this handler
// on "/" last, after the API prefix and the WebSocket route.
package web

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// content is the embedded front end. Every file below this directory is part
// of the binary, so a release stays one file and the daemon needs no working
// directory.
//
//go:embed index.html static
var content embed.FS

// indexFile is the shell served for "/" and for any path the embedded tree
// does not contain, so the UI can add client-side routes without a Go change.
const indexFile = "index.html"

// indexHTML is the shell, read once. It is compiled into the binary, so a
// failure here is a build mistake rather than a runtime condition.
var indexHTML = readAsset(indexFile)

// FS returns the embedded front end, for callers that need to walk it.
func FS() fs.FS { return content }

// Handler serves the embedded front end. An unknown path falls back to the
// shell, which is the single-page application convention; a non-GET request is
// refused.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "只支持 GET / HEAD 请求。", http.StatusMethodNotAllowed)
			return
		}

		name := assetName(r.URL.Path)
		data, err := content.ReadFile(name)
		if err != nil {
			name, data = indexFile, indexHTML
		}

		// The UI ships inside the daemon binary, so a stale copy is the one
		// failure mode that matters: never let a browser cache it.
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
	})
}

// assetName maps a URL path onto the embedded tree. path.Clean plus the
// leading slash keeps ".." from escaping the root.
func assetName(urlPath string) string {
	name := strings.TrimPrefix(path.Clean("/"+urlPath), "/")
	if name == "" {
		return indexFile
	}
	return name
}

// readAsset reads one embedded file.
func readAsset(name string) []byte {
	data, err := content.ReadFile(name)
	if err != nil {
		panic("web: embedded asset " + name + " is missing: " + err.Error())
	}
	return data
}
