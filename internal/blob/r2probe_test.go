//go:build r2probe

package blob

import (
	"context"
	"os"
	"strings"
	"testing"
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
	if err := r2.Delete(ctx, "probe.png"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := r2.Get(ctx, "probe.png"); err != ErrNotFound {
		t.Fatalf("after delete: %v", err)
	}
}
