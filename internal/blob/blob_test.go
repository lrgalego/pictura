package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestContentType(t *testing.T) {
	for name, want := range map[string]string{"a.png": "image/png", "b.JPG": "image/jpeg", "c.thumb.jpg": "image/jpeg", "d.webp": "image/webp", "e.gif": "image/gif", "f.bin": "application/octet-stream"} {
		if got := ContentType(name); got != want {
			t.Fatalf("%s: %s", name, got)
		}
	}
}

func TestFS(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "images")
	fs, err := NewFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := fs.Put(ctx, "a.png", []byte("PNG")); err != nil {
		t.Fatal(err)
	}
	if got, _ := fs.Get(ctx, "a.png"); string(got) != "PNG" || !fs.Exists("a.png") {
		t.Fatal("round trip")
	}
	if u, err := fs.URL(ctx, "a.png"); u != "" || err != nil {
		t.Fatal("fs streams through the app")
	}
	if _, err := fs.Get(ctx, "missing.png"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	for _, bad := range []string{"", "../etc/passwd", "x/y.png", ".", ".."} {
		if err := fs.Put(ctx, bad, nil); err != ErrNotFound {
			t.Fatalf("unsafe name %q accepted: %v", bad, err)
		}
		if _, err := fs.Get(ctx, bad); err != ErrNotFound {
			t.Fatalf("unsafe get %q: %v", bad, err)
		}
		if err := fs.Delete(ctx, bad); err != ErrNotFound {
			t.Fatalf("unsafe delete %q: %v", bad, err)
		}
		if fs.Exists(bad) {
			t.Fatalf("unsafe exists %q", bad)
		}
	}
	if err := fs.Delete(ctx, "a.png"); err != nil || fs.Exists("a.png") {
		t.Fatal("delete")
	}
	if err := fs.Delete(ctx, "a.png"); err != nil {
		t.Fatal("deleting twice is fine")
	}
	// A file where the directory should be makes the store unusable.
	_ = os.WriteFile(filepath.Join(dir, "blocker"), []byte("x"), 0o644)
	if _, err := NewFS(filepath.Join(dir, "blocker", "sub")); err == nil {
		t.Fatal("unusable dir should fail")
	}
}

// fakeS3 is the smallest S3 the client needs: one bucket, keys by path,
// no auth checking (the signature is still sent, which is what we assert).
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	types   map[string]string
	last    *http.Request
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = r.Clone(r.Context())
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch r.Method {
	case http.MethodPut:
		b, _ := io.ReadAll(r.Body)
		f.objects[key] = b
		f.types[key] = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		b, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
			return
		}
		w.Header().Set("Content-Type", f.types[key])
		w.Write(b)
	case http.MethodDelete:
		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestR2AgainstAFakeBucket(t *testing.T) {
	ctx := context.Background()
	fake := &fakeS3{objects: map[string][]byte{}, types: map[string]string{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	r2, err := NewR2(R2Config{AccessKeyID: "ak", SecretAccessKey: "sk", Bucket: "bucket", Endpoint: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Put(ctx, "a.png", []byte("PNG")); err != nil {
		t.Fatal(err)
	}
	if fake.types["a.png"] != "image/png" || !strings.HasPrefix(fake.last.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
		t.Fatalf("put should carry the content type and a SigV4 signature: %v", fake.last.Header)
	}
	got, err := r2.Get(ctx, "a.png")
	if err != nil || string(got) != "PNG" {
		t.Fatalf("get: %q %v", got, err)
	}
	if _, err := r2.Get(ctx, "missing.png"); err != ErrNotFound {
		t.Fatalf("missing: %v", err)
	}
	u, err := r2.URL(ctx, "a.png")
	if err != nil || !strings.HasPrefix(u, srv.URL+"/bucket/a.png?") || !strings.Contains(u, "X-Amz-Signature=") || !strings.Contains(u, "X-Amz-Expires=3600") {
		t.Fatalf("presigned url: %s %v", u, err)
	}
	resp, err := http.Get(u)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("presigned fetch: %v %v", resp, err)
	}
	if err := r2.Delete(ctx, "a.png"); err != nil {
		t.Fatal(err)
	}
	if _, ok := fake.objects["a.png"]; ok {
		t.Fatal("delete should remove the object")
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if err := r2.Put(ctx, bad, nil); err != ErrNotFound {
			t.Fatalf("unsafe put %q", bad)
		}
		if _, err := r2.Get(ctx, bad); err != ErrNotFound {
			t.Fatalf("unsafe get %q", bad)
		}
		if err := r2.Delete(ctx, bad); err != ErrNotFound {
			t.Fatalf("unsafe delete %q", bad)
		}
		if _, err := r2.URL(ctx, bad); err != ErrNotFound {
			t.Fatalf("unsafe url %q", bad)
		}
	}
	// A server error is surfaced, not swallowed as not-found.
	srv.Close()
	if _, err := r2.Get(ctx, "a.png"); err == nil || err == ErrNotFound {
		t.Fatalf("connection failure: %v", err)
	}
}

func TestR2Config(t *testing.T) {
	if _, err := NewR2(R2Config{}); err == nil {
		t.Fatal("empty config should fail")
	}
	if _, err := NewR2(R2Config{AccessKeyID: "a", SecretAccessKey: "s", Bucket: "b"}); err == nil {
		t.Fatal("account id is required without an endpoint override")
	}
	r2, err := NewR2(R2Config{AccountID: "acct", AccessKeyID: "a", SecretAccessKey: "s", Bucket: "b"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := r2.URL(context.Background(), "x.png")
	if err != nil || !strings.HasPrefix(u, "https://acct.r2.cloudflarestorage.com/b/x.png?") {
		t.Fatalf("account endpoint: %s %v", u, err)
	}
}
