// Package storage wraps the S3-compatible object store — MinIO locally, Cloudflare R2 in production
// — that holds every uploaded and normalized dataset file, per architecture.md's Data Architecture.
//
// This package holds only the object-storage client itself. It does not decide bucket layout policy
// beyond the key convention documented on Client.Key; ingest/normalization (FEAT-004, Wave 3) is a
// separate concern layered on top of the raw upload this wave writes.
package storage

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Client is a thin wrapper around the AWS S3 SDK, pointed at either MinIO (local) or Cloudflare R2
// (production) via a custom endpoint — both speak the S3 API, per architecture.md.
type Client struct {
	s3     *s3.Client
	bucket string
}

// Config holds the connection details for the object store. Field names mirror the
// S3_* environment variables documented in server/.env.example.
type Config struct {
	Endpoint        string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	Region          string
}

// NewClient constructs a Client from cfg.
//
// UsePathStyle is required for MinIO (and works fine against R2): virtual-hosted-style bucket
// addressing needs DNS wildcarding neither local MinIO nor a plain custom R2 endpoint provides.
func NewClient(cfg Config) *Client {
	awsCfg := aws.Config{
		Region: cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.SecretAccessKey, "",
		),
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(cfg.Endpoint)
		o.UsePathStyle = true
	})

	return &Client{s3: client, bucket: cfg.Bucket}
}

// DatasetKey builds the object key for an originally-uploaded file, keyed by workspace and dataset
// so a workspace's deletion cascade (FEAT-043, Phase 1 but implemented outside this wave) can later
// delete by prefix.
//
// Convention: workspaces/{workspaceID}/datasets/{datasetID}/original/{filename}
func DatasetKey(workspaceID, datasetID, filename string) string {
	return fmt.Sprintf("workspaces/%s/datasets/%s/original/%s", workspaceID, datasetID, filename)
}

// PutObject uploads content (of the given size) to key, streaming rather than buffering the whole
// file in memory — required for files up to the 500 MB ceiling.
func (c *Client) PutObject(ctx context.Context, key string, content io.Reader, size int64, contentType string) error {
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(c.bucket),
		Key:           aws.String(key),
		Body:          content,
		ContentLength: aws.Int64(size),
		ContentType:   aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("storage: putting object %q: %w", key, err)
	}
	return nil
}

// Bucket reports the configured bucket name.
func (c *Client) Bucket() string {
	return c.bucket
}

// GetObject retrieves the object at key. Callers MUST Close the returned reader.
//
// Added by FEAT-004 (Wave 3): the Ingest Pipeline reads back the originally-uploaded file FEAT-003
// wrote (at DatasetKey) so it can be loaded into the engine and normalized into Parquet. Additive to
// this package's read path only — PutObject and DatasetKey's semantics are unchanged.
func (c *Client) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: getting object %q: %w", key, err)
	}
	return out.Body, nil
}

// DeleteObject removes the object at key.
//
// Added for FEAT-048 (Single-Dataset Deletion): confirmed operationally that object storage does NOT
// cascade when the owning Dataset row is deleted from Postgres — every uploaded original and
// normalized Parquet file is left behind unless removed by an explicit call like this one. A dataset
// deletion that only removes database rows and leaves these objects in R2/MinIO would silently break
// the provable-deletion promise this feature exists to uphold.
//
// Deleting a key that does not exist is not an error on S3-compatible stores (this call succeeds),
// which matters here: a dataset that failed before its normalized Parquet was ever written still has
// an original object to remove, and callers should not have to know which objects exist before
// asking for them to be gone.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("storage: deleting object %q: %w", key, err)
	}
	return nil
}

// DeleteObjectsByPrefix removes every object whose key starts with prefix — the workspace-scope
// sweep FEAT-043's deletion cascade drives ("object store contents"). Object storage has no foreign
// keys and does not cascade on its own (the FEAT-048 lesson), so a workspace's objects are removed
// by an explicit prefix sweep, never assumed to disappear with the workspace row.
//
// Listing is paginated (ListObjectsV2) and deletion is batched (DeleteObjects, up to 1000 keys per
// call) so a large workspace does not require one DeleteObject round-trip per file. A prefix with no
// objects is not an error: the workspace may never have uploaded anything, and the cascade must be
// idempotent so a retried deletion does not fail on an already-empty prefix.
func (c *Client) DeleteObjectsByPrefix(ctx context.Context, prefix string) error {
	var continuationToken *string
	for {
		listOut, err := c.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(c.bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: continuationToken,
		})
		if err != nil {
			return fmt.Errorf("storage: listing objects under %q: %w", prefix, err)
		}

		if len(listOut.Contents) > 0 {
			identifiers := make([]types.ObjectIdentifier, 0, len(listOut.Contents))
			for _, obj := range listOut.Contents {
				if obj.Key != nil {
					identifiers = append(identifiers, types.ObjectIdentifier{Key: obj.Key})
				}
			}
			if len(identifiers) > 0 {
				if _, err := c.s3.DeleteObjects(ctx, &s3.DeleteObjectsInput{
					Bucket: aws.String(c.bucket),
					Delete: &types.Delete{Objects: identifiers, Quiet: aws.Bool(true)},
				}); err != nil {
					return fmt.Errorf("storage: deleting objects under %q: %w", prefix, err)
				}
			}
		}

		if !aws.ToBool(listOut.IsTruncated) || listOut.NextContinuationToken == nil {
			break
		}
		continuationToken = listOut.NextContinuationToken
	}
	return nil
}

// NormalizedDatasetKey builds the object key for a dataset's normalized, canonical Parquet form —
// the durable artifact FEAT-004's Ingest Pipeline writes, independent of the engine's lifecycle per
// architecture.md's Data Architecture. Mirrors DatasetKey's convention.
func NormalizedDatasetKey(workspaceID, datasetID string) string {
	return fmt.Sprintf("workspaces/%s/datasets/%s/normalized/data.parquet", workspaceID, datasetID)
}
