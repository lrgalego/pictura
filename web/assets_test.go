package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestAssetVersioning(t *testing.T) {
	if len(appAssetVersion) != 10 || appAssetVersion != hashFS(staticFS) {
		t.Fatalf("app asset version should be a stable 10-char hash, got %q", appAssetVersion)
	}
	if dsAssetVersion == "" {
		t.Fatal("design-system version should never be empty")
	}
	if got := versioned("/static/app/app.css"); got != "/static/app/app.css?v="+appAssetVersion {
		t.Fatalf("app asset url: %s", got)
	}
	if got := versioned("/static/css/app.css"); !strings.HasPrefix(got, "/static/css/app.css?v=") {
		t.Fatalf("ds asset url: %s", got)
	}

	e := newEnv(t)
	resp, body := e.get("/")
	if resp.StatusCode != http.StatusOK {
		t.Fatal(resp.Status)
	}
	for _, want := range []string{`href="/static/css/app.css?v=`, `href="/static/app/app.css?v=` + appAssetVersion, `src="/static/htmx.min.js?v=`, `src="/static/js/ds.js?v=`, `href="/static/app/favicon.svg?v=`} {
		if !strings.Contains(body, want) {
			t.Fatalf("page should link versioned assets, missing %s", want)
		}
	}
	// Versioned URLs are immutable; bare ones must revalidate.
	for path, want := range map[string]string{
		"/static/app/app.css?v=" + appAssetVersion: "immutable",
		"/static/app/app.css":                      "no-cache",
		"/static/css/app.css?v=x":                  "immutable",
		"/static/htmx.min.js":                      "no-cache",
	} {
		resp, _ := e.get(path)
		if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Cache-Control"), want) {
			t.Fatalf("%s: %d %q", path, resp.StatusCode, resp.Header.Get("Cache-Control"))
		}
	}
}
