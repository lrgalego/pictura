package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// R2 keeps blobs in a Cloudflare R2 bucket through its S3-compatible API.
// Reads by browsers go straight to R2 through presigned URLs, so the VM
// and the tunnel never carry image bytes; the app itself only fetches a
// blob when it needs the pixels (references for the model, PDF, zip).
type R2 struct {
	client  *s3.Client
	presign *s3.PresignClient
	bucket  string
	now     func() time.Time
}

// Presigned URLs are stable within a window so browsers can cache what
// they point at. A URL is signed as of the start of the current window and
// stays valid for two windows, so it is good for at least a full window
// after it is handed out; a browser re-downloads a blob at most about once
// per window. Blob names are never reused (a new drawing or clip is a new
// name), which is what makes caching them for good safe.
const (
	SignWindow = 12 * time.Hour
	signTTL    = 2 * SignWindow
	// Immutable is the cache policy of every blob: its name never points
	// at different bytes.
	Immutable = "private, max-age=31536000, immutable"
)

// WindowEnd is when URLs signed at t stop being handed out, so a redirect
// to one may be cached until then.
func WindowEnd(t time.Time) time.Time {
	return t.UTC().Truncate(SignWindow).Add(SignWindow)
}

// fixedTime signs as of a set time instead of now; that is all it takes
// for two presigns of one object in one window to be the same URL.
type fixedTime struct {
	signer *v4.Signer
	at     time.Time
}

func (f fixedTime) PresignHTTP(ctx context.Context, creds aws.Credentials, r *http.Request, payloadHash, service, region string, _ time.Time, optFns ...func(*v4.SignerOptions)) (string, http.Header, error) {
	return f.signer.PresignHTTP(ctx, creds, r, payloadHash, service, region, f.at, optFns...)
}

// R2Config is what an R2 bucket needs: the account, an API token with
// object read/write on the bucket, and the bucket name.
type R2Config struct {
	AccountID       string
	AccessKeyID     string
	SecretAccessKey string
	Bucket          string
	// Endpoint overrides the account endpoint (tests).
	Endpoint string
}

// NewR2 builds the client. It does no network call; the first Put or Get
// surfaces bad credentials.
func NewR2(cfg R2Config) (*R2, error) {
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || cfg.SecretAccessKey == "" {
		return nil, errors.New("r2: bucket, access key id and secret access key are required")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		if cfg.AccountID == "" {
			return nil, errors.New("r2: account id is required")
		}
		endpoint = fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID)
	}
	client := s3.New(s3.Options{
		Region:       "auto",
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		UsePathStyle: true,
	})
	return &R2{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, now: time.Now}, nil
}

func (r *R2) Put(ctx context.Context, name string, data []byte) error {
	if !safe(name) {
		return ErrNotFound
	}
	_, err := r.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:       aws.String(r.bucket),
		Key:          aws.String(name),
		Body:         bytes.NewReader(data),
		ContentType:  aws.String(ContentType(name)),
		CacheControl: aws.String(Immutable),
	})
	return err
}

func (r *R2) Get(ctx context.Context, name string) ([]byte, error) {
	if !safe(name) {
		return nil, ErrNotFound
	}
	out, err := r.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(name)})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func (r *R2) Delete(ctx context.Context, name string) error {
	if !safe(name) {
		return ErrNotFound
	}
	_, err := r.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(name)})
	return err
}

// URL is a presigned GET, the same for a blob all through a signing
// window. It also asks R2 to answer with the immutable cache policy, which
// covers objects stored before Put set it.
func (r *R2) URL(ctx context.Context, name string) (string, error) {
	if !safe(name) {
		return "", ErrNotFound
	}
	at := r.now().UTC().Truncate(SignWindow)
	in := &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(name), ResponseCacheControl: aws.String(Immutable)}
	req, err := r.presign.PresignGetObject(ctx, in, func(o *s3.PresignOptions) {
		o.Expires = signTTL
		o.Presigner = fixedTime{signer: v4.NewSigner(), at: at}
	})
	if err != nil {
		return "", err
	}
	return req.URL, nil
}
