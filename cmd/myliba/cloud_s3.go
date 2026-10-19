package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3-compatible API. Object names are fixed to
// <prefix>/{blocks,catalog}/<sha256>, so a storage node cannot write outside
// its configured namespace through the cloud protocol.
type s3Backend struct {
	endpoint                                                   *url.URL
	bucket, prefix, region, accessKey, secretKey, sessionToken string
	virtualHost                                                bool
	client                                                     *http.Client
}

func newS3Backend() (*s3Backend, error) {
	endpoint, err := validBackendURL(strings.TrimSpace(os.Getenv("MYLIBA_S3_ENDPOINT")))
	if err != nil {
		return nil, err
	}
	if endpoint.Path != "" && endpoint.Path != "/" {
		return nil, errors.New("S3 endpoint must not contain a path")
	}
	b := &s3Backend{
		endpoint: endpoint, bucket: os.Getenv("MYLIBA_S3_BUCKET"), prefix: strings.Trim(os.Getenv("MYLIBA_S3_PREFIX"), "/"),
		region: os.Getenv("MYLIBA_S3_REGION"), accessKey: os.Getenv("MYLIBA_S3_ACCESS_KEY_ID"),
		secretKey: os.Getenv("MYLIBA_S3_SECRET_ACCESS_KEY"), sessionToken: os.Getenv("MYLIBA_S3_SESSION_TOKEN"),
		client: cloudBackendHTTPClient(),
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MYLIBA_S3_ADDRESSING"))) {
	case "", "path":
	case "virtual":
		b.virtualHost = true
	default:
		return nil, errors.New("MYLIBA_S3_ADDRESSING must be path or virtual")
	}
	if b.bucket == "" || strings.ContainsAny(b.bucket, "/\\?#") || b.region == "" || b.accessKey == "" || b.secretKey == "" {
		return nil, errors.New("S3 bucket, region, access key and secret key are required")
	}
	for _, segment := range strings.Split(b.prefix, "/") {
		if segment == "." || segment == ".." || strings.ContainsAny(segment, "\\?#") {
			return nil, errors.New("invalid S3 prefix")
		}
	}
	return b, nil
}

func (b *s3Backend) objectKey(kind, id string) string {
	key := kind + "/" + id
	if b.prefix != "" {
		key = b.prefix + "/" + key
	}
	return key
}

func s3HMAC(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return mac.Sum(nil)
}

func s3CanonicalQuery(values url.Values) string {
	return strings.ReplaceAll(values.Encode(), "+", "%20")
}

func (b *s3Backend) request(method, key string, query url.Values, payload []byte) (*http.Response, error) {
	u := *b.endpoint
	if b.virtualHost {
		u.Host = b.bucket + "." + b.endpoint.Host
		u.Path = "/"
		if key != "" {
			u.Path += key
		}
	} else {
		u.Path = "/" + b.bucket
		if key != "" {
			u.Path += "/" + key
		}
	}
	u.RawQuery = s3CanonicalQuery(query)
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	date := now.Format("20060102")
	stamp := now.Format("20060102T150405Z")
	payloadHash := cloudHash(payload)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", stamp)
	if b.sessionToken != "" {
		req.Header.Set("x-amz-security-token", b.sessionToken)
	}
	canonicalHeaders := "host:" + req.URL.Host + "\n" + "x-amz-content-sha256:" + payloadHash + "\n" + "x-amz-date:" + stamp + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	if b.sessionToken != "" {
		canonicalHeaders += "x-amz-security-token:" + b.sessionToken + "\n"
		signedHeaders += ";x-amz-security-token"
	}
	canonical := method + "\n" + req.URL.EscapedPath() + "\n" + u.RawQuery + "\n" + canonicalHeaders + "\n" + signedHeaders + "\n" + payloadHash
	scope := date + "/" + b.region + "/s3/aws4_request"
	stringToSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + cloudHash([]byte(canonical))
	signingKey := s3HMAC(s3HMAC(s3HMAC(s3HMAC([]byte("AWS4"+b.secretKey), date), b.region), "s3"), "aws4_request")
	signature := hex.EncodeToString(s3HMAC(signingKey, stringToSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+b.accessKey+"/"+scope+",SignedHeaders="+signedHeaders+",Signature="+signature)
	return b.client.Do(req)
}

func (b *s3Backend) Put(kind, id string, data []byte) error {
	response, err := b.request(http.MethodPut, b.objectKey(kind, id), nil, data)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 && response.StatusCode != 201 && response.StatusCode != 204 {
		return fmt.Errorf("S3 PUT: HTTP %d", response.StatusCode)
	}
	return nil
}

func (b *s3Backend) Get(kind, id string) ([]byte, error) {
	response, err := b.request(http.MethodGet, b.objectKey(kind, id), nil, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, fmt.Errorf("S3 GET: HTTP %d", response.StatusCode)
	}
	limit := int64(cloudMaxBlobSize)
	if kind == "catalog" {
		limit = cloudMaxRecordSize
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("S3 object too large")
	}
	return data, nil
}

func (b *s3Backend) List(kind string) ([]string, error) {
	prefix := b.objectKey(kind, "")
	ids := []string{}
	seen := map[string]bool{}
	continuation := ""
	for {
		query := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if continuation != "" {
			query.Set("continuation-token", continuation)
		}
		response, err := b.request(http.MethodGet, "", query, nil)
		if err != nil {
			return nil, err
		}
		if response.StatusCode != 200 {
			response.Body.Close()
			return nil, fmt.Errorf("S3 LIST: HTTP %d", response.StatusCode)
		}
		var listing struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key string `xml:"Key"`
			} `xml:"Contents"`
		}
		err = xml.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&listing)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		for _, item := range listing.Contents {
			id := strings.TrimPrefix(item.Key, prefix)
			if item.Key != prefix+id || !validCloudHash(id) || seen[id] {
				continue
			}
			ids = append(ids, id)
			seen[id] = true
		}
		if !listing.IsTruncated {
			break
		}
		if listing.NextContinuationToken == "" || listing.NextContinuationToken == continuation {
			return nil, errors.New("invalid S3 continuation token")
		}
		continuation = listing.NextContinuationToken
	}
	sort.Strings(ids)
	return ids, nil
}
