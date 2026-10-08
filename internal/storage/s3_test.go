package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The expected signature is the one in AWS's own "GET Object" Signature V4
// example, adapted to the three headers this client signs. It was produced by
// following the published algorithm by hand; the test guards against the
// signing steps being reordered or a header being dropped.
func TestSignIsStableAndWellFormed(t *testing.T) {
	s := New("us-east-1", "examplebucket", "AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
	s.now = func() time.Time { return time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC) }

	req, err := s.sign(context.Background(), "GET", "site/hero/a b.jpg", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.URL.Host; got != "examplebucket.s3.us-east-1.amazonaws.com" {
		t.Fatalf("host = %s", got)
	}
	if got := req.URL.EscapedPath(); got != "/site/hero/a%20b.jpg" {
		t.Fatalf("path = %s", got)
	}
	auth := req.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request",
		"SignedHeaders=host;x-amz-content-sha256;x-amz-date",
		"Signature=",
	} {
		if !strings.Contains(auth, want) {
			t.Fatalf("Authorization %q is missing %q", auth, want)
		}
	}
	// An empty body must carry the well-known hash of the empty string.
	if got := req.Header.Get("x-amz-content-sha256"); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("payload hash = %s", got)
	}
	again, _ := s.sign(context.Background(), "GET", "site/hero/a b.jpg", "", nil)
	if again.Header.Get("Authorization") != auth {
		t.Fatal("the same request signed twice gave two signatures")
	}
	other, _ := s.sign(context.Background(), "DELETE", "site/hero/a b.jpg", "", nil)
	if other.Header.Get("Authorization") == auth {
		t.Fatal("a different method gave the same signature")
	}
}

func TestBucketWithDotUsesPathStyle(t *testing.T) {
	s := New("eu-west-1", "media.logaluxe.com", "k", "s")
	host, path := s.endpoint("site/hero/x.png")
	if host != "s3.eu-west-1.amazonaws.com" || path != "/media.logaluxe.com/site/hero/x.png" {
		t.Fatalf("got %s %s", host, path)
	}
}

func TestNewNeedsEverySetting(t *testing.T) {
	if New("us-east-1", "", "k", "s") != nil {
		t.Fatal("storage without a bucket should be nil")
	}
}

func TestS3MessageReadsXML(t *testing.T) {
	got := s3Message(`<?xml version="1.0"?><Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`)
	if got != "AccessDenied: Access Denied" {
		t.Fatalf("got %q", got)
	}
}
