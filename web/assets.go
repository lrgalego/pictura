package web

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
)

// Asset URLs carry a version query so an edge cache (Cloudflare caches
// .css and .js for hours by default) can never serve yesterday's stylesheet
// with today's HTML: a new build is a new URL. Our own files are versioned
// by a hash of their contents; the design system's by its module version.

// appAssetVersion is a short hash over every embedded static file.
var appAssetVersion = hashFS(staticFS)

func hashFS(fsys fs.FS) string {
	var names []string
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			names = append(names, p)
		}
		return nil
	})
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		b, _ := fs.ReadFile(fsys, n)
		h.Write([]byte(n))
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// dsAssetVersion is the htmx-ds module version from the build info, or
// "dev" when running from source without one.
var dsAssetVersion = func() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/lrgalego/htmx-ds" {
				if d.Replace != nil {
					return "dev"
				}
				return strings.TrimPrefix(d.Version, "v")
			}
		}
	}
	return "dev"
}()

// versioned appends the right version to an asset path.
func versioned(path string) string {
	if strings.HasPrefix(path, "/static/app/") {
		return path + "?v=" + appAssetVersion
	}
	return path + "?v=" + dsAssetVersion
}

// cacheAssets sets cache headers: a versioned URL is immutable for a year,
// a bare one must be revalidated every time. The header is applied as the
// response is written so it wins over whatever the inner handler set.
func cacheAssets(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy := "no-cache"
		if r.URL.Query().Get("v") != "" {
			policy = "public, max-age=31536000, immutable"
		}
		next.ServeHTTP(&cachePolicyWriter{ResponseWriter: w, policy: policy}, r)
	})
}

type cachePolicyWriter struct {
	http.ResponseWriter
	policy string
	done   bool
}

func (c *cachePolicyWriter) WriteHeader(code int) {
	if !c.done {
		c.done = true
		c.Header().Set("Cache-Control", c.policy)
	}
	c.ResponseWriter.WriteHeader(code)
}

func (c *cachePolicyWriter) Write(b []byte) (int, error) {
	if !c.done {
		c.WriteHeader(http.StatusOK)
	}
	return c.ResponseWriter.Write(b)
}
