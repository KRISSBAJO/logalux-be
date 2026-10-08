// Package storage talks to Amazon S3 with plain HTTP and Signature Version 4.
// It covers the three calls the API needs (put, get, delete) so the service
// does not carry the whole AWS SDK.
package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type S3 struct {
	Region    string
	Bucket    string
	AccessKey string
	SecretKey string

	client *http.Client
	now    func() time.Time // replaced in tests
}

// New returns nil when storage is not configured, so callers can report that
// clearly instead of failing on the first upload.
func New(region, bucket, accessKey, secretKey string) *S3 {
	if region == "" || bucket == "" || accessKey == "" || secretKey == "" {
		return nil
	}
	return &S3{Region: region, Bucket: bucket, AccessKey: accessKey, SecretKey: secretKey,
		client: &http.Client{Timeout: 25 * time.Second}, now: time.Now}
}

// Put stores an object. The bucket stays private; the API serves the bytes.
func (s *S3) Put(ctx context.Context, key, contentType string, body []byte) error {
	res, err := s.do(ctx, http.MethodPut, key, contentType, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return check(res, http.StatusOK)
}

// Get returns the object's response. The caller closes the body.
func (s *S3) Get(ctx context.Context, key string) (*http.Response, error) {
	res, err := s.do(ctx, http.MethodGet, key, "", nil)
	if err != nil {
		return nil, err
	}
	if err := check(res, http.StatusOK); err != nil {
		res.Body.Close()
		return nil, err
	}
	return res, nil
}

func (s *S3) Delete(ctx context.Context, key string) error {
	res, err := s.do(ctx, http.MethodDelete, key, "", nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return check(res, http.StatusNoContent, http.StatusOK)
}

func check(res *http.Response, want ...int) error {
	for _, w := range want {
		if res.StatusCode == w {
			return nil
		}
	}
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 600))
	return fmt.Errorf("s3: %s: %s", res.Status, s3Message(string(msg)))
}

// s3Message pulls the short code and message out of S3's XML error body.
func s3Message(xml string) string {
	pick := func(tag string) string {
		a := strings.Index(xml, "<"+tag+">")
		b := strings.Index(xml, "</"+tag+">")
		if a < 0 || b < a {
			return ""
		}
		return xml[a+len(tag)+2 : b]
	}
	if code := pick("Code"); code != "" {
		return code + ": " + pick("Message")
	}
	return strings.TrimSpace(xml)
}

func (s *S3) do(ctx context.Context, method, key, contentType string, body []byte) (*http.Response, error) {
	req, err := s.sign(ctx, method, key, contentType, body)
	if err != nil {
		return nil, err
	}
	return s.client.Do(req)
}

// endpoint uses the bucket as the host name, except for bucket names with a
// dot, which do not match the wildcard TLS certificate.
func (s *S3) endpoint(key string) (host, path string) {
	if strings.Contains(s.Bucket, ".") {
		return "s3." + s.Region + ".amazonaws.com", "/" + s.Bucket + "/" + escapePath(key)
	}
	return s.Bucket + ".s3." + s.Region + ".amazonaws.com", "/" + escapePath(key)
}

func escapePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = strings.ReplaceAll(url.QueryEscape(p), "+", "%20")
	}
	return strings.Join(parts, "/")
}

func (s *S3) sign(ctx context.Context, method, key, contentType string, body []byte) (*http.Request, error) {
	host, path := s.endpoint(key)
	req, err := http.NewRequestWithContext(ctx, method, "https://"+host+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	// NewRequest parses the path; keep our own escaping so the signature matches the wire.
	req.URL.RawPath = path

	t := s.now().UTC()
	amzDate := t.Format("20060102T150405Z")
	day := t.Format("20060102")
	payloadHash := hexSHA(body)

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.ContentLength = int64(len(body))

	const signed = "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{
		method, path, "",
		"host:" + host + "\nx-amz-content-sha256:" + payloadHash + "\nx-amz-date:" + amzDate + "\n",
		signed, payloadHash,
	}, "\n")
	scope := day + "/" + s.Region + "/s3/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA([]byte(canonical))

	k := mac([]byte("AWS4"+s.SecretKey), day)
	k = mac(k, s.Region)
	k = mac(k, "s3")
	k = mac(k, "aws4_request")
	sig := hex.EncodeToString(mac(k, toSign))

	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+s.AccessKey+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
	return req, nil
}

func mac(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
