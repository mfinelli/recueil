/*
 * recueil: self-hosted webpage bookmarker and archiver
 * Copyright © 2026 Mario Finelli
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as published by
 * the Free Software Foundation, either version 3 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program. If not, see <https://www.gnu.org/licenses/>.
 */

// Package r2 is the backend's own client for R2, separate from (and using a
// different credential-consumption path than) the Worker's own R2 access.
// The Worker only ever issues presigned URLs (terraform/index.js's
// hand-rolled SigV4 -- no dependency allowed there); the backend pulls the
// actual capture blob down and deletes it once ingested which is real,
// ongoing R2 traffic the backend is free to make with a real SDK rather than
// hand-rolled signing.
//
// Uses the same manually-provisioned R2 API token already used by the
// Worker for presigned uploads (see terraform/README.md's "Manual setup: R2
// API credentials" -- this package is a second consumer of that one
// credential, not a second credential to provision).
package r2

import (
	"context"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Config struct {
	AccountID       string
	BucketName      string
	AccessKeyID     string
	AccessKeySecret string
}

type Client struct {
	s3     *s3.Client
	bucket string
}

// New builds a Client against R2's S3-compatible API. UsePathStyle is set
// explicitly rather than left to the SDK's own virtual-host-by-default
// resolution: R2's addressing scheme is
// `https://<accountID>.r2.cloudflarestorage.com/<bucket>/<key>` (bucket in
// the path). R2 does also support virtual-hosted-style addressing for
// bucket names that happen to be valid hostname labels, but there's no
// reason to depend on that working when the path-style form is already
// known-good and consistent with how the Worker addresses the same bucket.
func New(cfg Config) (*Client, error) {
	awsCfg, err := config.LoadDefaultConfig(context.Background(),
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			cfg.AccessKeyID, cfg.AccessKeySecret, "",
		)),
		// Required by the SDK, ignored by R2 -- "auto" is the same literal
		// value the Worker's own SigV4 signer uses for the same reason.
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, fmt.Errorf("r2: loading AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(fmt.Sprintf("https://%s.r2.cloudflarestorage.com", cfg.AccountID))
		o.UsePathStyle = true
	})

	return &Client{s3: client, bucket: cfg.BucketName}, nil
}

// Get retrieves an object's full contents. The caller must Close the
// returned ReadCloser.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("r2: getting object %q: %w", key, err)
	}
	return out.Body, nil
}

// Delete removes an object. Called only after the backend has durably
// written the object's contents to local disk and committed the
// corresponding Postgres row/
func (c *Client) Delete(ctx context.Context, key string) error {
	_, err := c.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("r2: deleting object %q: %w", key, err)
	}
	return nil
}
