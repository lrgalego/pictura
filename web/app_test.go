package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestInstallableApp(t *testing.T) {
	e := newEnv(t)
	for path, want := range map[string]string{
		"/manifest.webmanifest":             "application/manifest+json",
		"/sw.js":                            "text/javascript; charset=utf-8",
		"/favicon.ico":                      "image/x-icon",
		"/apple-touch-icon.png":             "image/png",
		"/apple-touch-icon-precomposed.png": "image/png",
	} {
		resp, body := e.get(path)
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != want || body == "" {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
	}
	if resp, _ := e.get("/sw.js"); resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("the service worker is never cached stale")
	}
	_, body := e.get("/manifest.webmanifest")
	var m struct {
		Name, StartURL, Display string
		Icons                   []struct{ Src, Sizes, Purpose string }
	}
	if err := json.Unmarshal([]byte(strings.ReplaceAll(body, "start_url", "starturl")), &m); err != nil {
		t.Fatal(err)
	}
	if m.Name != "Pictura" || m.StartURL != "/stories" || m.Display != "standalone" || len(m.Icons) < 3 {
		t.Fatalf("manifest: %+v", m)
	}
	for _, ic := range m.Icons {
		if resp, _ := e.get(ic.Src); resp.StatusCode != http.StatusOK {
			t.Fatalf("icon %s: %d", ic.Src, resp.StatusCode)
		}
	}
	_, page := e.get("/")
	for _, want := range []string{`rel="manifest" href="/manifest.webmanifest"`, `rel="apple-touch-icon"`, `name="apple-mobile-web-app-capable" content="yes"`, `name="theme-color" content="#fff9ef"`, "/static/app/pwa.js"} {
		if !strings.Contains(page, want) {
			t.Fatalf("head missing %q", want)
		}
	}
	if strings.Contains(page, `class="tabbar"`) {
		t.Fatal("no tab bar before signing in")
	}
}

func TestTabBar(t *testing.T) {
	e := newEnv(t)
	e.signup("tabs")
	for path, current := range map[string]string{"/stories": "/stories", "/stories/new": "/stories/new", "/cast": "/cast"} {
		_, body := e.get(path)
		if !strings.Contains(body, `class="tabbar"`) || !strings.Contains(body, `href="`+current+`" class="tabbar__tab`) {
			t.Fatalf("%s: no tab bar", path)
		}
		i := strings.Index(body, `href="`+current+`" class="tabbar__tab`)
		if tag := body[i : i+strings.Index(body[i:], ">")]; !strings.Contains(tag, `aria-current="page"`) {
			t.Fatalf("%s: its tab is not current: %s", path, tag)
		}
	}
	base, _ := e.finished(t)
	_, body := e.get(base + "/read")
	if strings.Contains(body, `class="tabbar"`) || !strings.Contains(body, "site--immersive") {
		t.Fatal("the reader takes the whole screen")
	}
	_, body = e.get(base + "/book")
	if !strings.Contains(body, `class="story-head__back"`) || !strings.Contains(body, `aria-label="Edit script"`) {
		t.Fatal("a story leads back to the library and keeps its actions")
	}
}
