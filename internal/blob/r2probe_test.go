//go:build r2probe

package blob

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestR2Probe talks to the real bucket named in the environment: put, get,
// presign, delete one tiny object. Run by hand with -tags r2probe.
func TestR2Probe(t *testing.T) {
	r2, err := NewR2(R2Config{AccountID: os.Getenv("R2_ACCOUNT_ID"), AccessKeyID: os.Getenv("R2_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("R2_SECRET_ACCESS_KEY"), Bucket: os.Getenv("R2_BUCKET")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r2.Put(ctx, "probe.png", []byte("probe")); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := r2.Get(ctx, "probe.png")
	if err != nil || string(got) != "probe" {
		t.Fatalf("get: %q %v", got, err)
	}
	u, err := r2.URL(ctx, "probe.png")
	if err != nil || !strings.Contains(u, "X-Amz-Signature") {
		t.Fatalf("presign: %s %v", u, err)
	}
	t.Logf("presigned url host: %s", strings.SplitN(strings.TrimPrefix(u, "https://"), "/", 2)[0])
	// R2 accepts a URL signed as of the window start (up to a window ago)
	// and answers with the immutable cache policy browsers need.
	resp, err := http.Get(u)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Cache-Control") != Immutable {
		t.Fatalf("fetch: %d, Cache-Control %q", resp.StatusCode, resp.Header.Get("Cache-Control"))
	}
	t.Logf("signed at window start %s; R2 answered %d with Cache-Control %q", time.Now().UTC().Truncate(SignWindow).Format(time.RFC3339), resp.StatusCode, resp.Header.Get("Cache-Control"))
	if again, _ := r2.URL(ctx, "probe.png"); again != u {
		t.Fatal("a second presign in the same window should be the same url")
	}
	if err := r2.Delete(ctx, "probe.png"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r2.Get(ctx, "probe.png"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}
