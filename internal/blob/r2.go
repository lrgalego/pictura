package blob

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
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
	// TTL of presigned URLs. Long enough for a page full of thumbnails to
	// load and for a browser to revisit within a session.
	ttl time.Duration
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
	return &R2{client: client, presign: s3.NewPresignClient(client), bucket: cfg.Bucket, ttl: time.Hour}, nil
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
		CacheControl: aws.String("private, max-age=31536000, immutable"),
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

// URL is a presigned GET valid for the store's TTL.
func (r *R2) URL(ctx context.Context, name string) (string, error) {
	if !safe(name) {
		return "", ErrNotFound
	}
	req, err := r.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(r.bucket), Key: aws.String(name)}, s3.WithPresignExpires(r.ttl))
	if err != nil {
		return "", err
	}
	return req.URL, nil
}
