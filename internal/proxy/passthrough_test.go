package proxy

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"gpt-load/internal/models"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"gorm.io/datatypes"
)

func TestPassthroughConnectionHeadersAndMultivalueHeaders(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	headers := http.Header{
		"Connection":    {"X-Private-Hop, keep-alive"},
		"X-Private-Hop": {"private"},
		"Keep-Alive":    {"timeout=5"},
		"Set-Cookie":    {"a=1", "b=2"},
		"Content-Type":  {"application/octet-stream"},
	}
	response := &http.Response{StatusCode: http.StatusTeapot, Header: headers, Body: io.NopCloser(bytes.NewReader([]byte{0, 255, 1}))}
	(&ProxyServer{}).handlePassthroughResponse(c, response)
	if recorder.Code != http.StatusTeapot || !bytes.Equal(recorder.Body.Bytes(), []byte{0, 255, 1}) {
		t.Fatalf("response changed: %d, %q", recorder.Code, recorder.Body.Bytes())
	}
	if len(recorder.Header().Values("Set-Cookie")) != 2 {
		t.Fatal("multiple cookies were lost")
	}
	for _, name := range []string{"Connection", "X-Private-Hop", "Keep-Alive"} {
		if recorder.Header().Get(name) != "" {
			t.Fatalf("hop header %s was forwarded", name)
		}
	}
	if headers.Get("Connection") == "" {
		t.Fatal("upstream headers were modified")
	}
}

func TestPassthroughErrorRedactsShortKey(t *testing.T) {
	response := &http.Response{Header: http.Header{"Content-Type": {"text/plain"}, "Content-Length": {"16"}, "X-Error": {"invalid abc"}}}
	body := []byte("invalid key: abc")
	actual := redactPassthroughError(response, body, "abc")
	if bytes.Contains(actual, []byte("abc")) || response.Header.Get("X-Error") != "invalid [REDACTED]" {
		t.Fatalf("short key leaked: %q, %v", actual, response.Header)
	}
	if response.Header.Get("Content-Length") != "" {
		t.Fatal("stale length was preserved")
	}
}

func encodePassthroughTestBody(t *testing.T, body []byte, encodings ...string) []byte {
	t.Helper()
	for _, encoding := range encodings {
		var buffer bytes.Buffer
		var writer io.WriteCloser
		var err error
		switch encoding {
		case "gzip":
			writer = gzip.NewWriter(&buffer)
		case "deflate":
			writer = zlib.NewWriter(&buffer)
		case "rawdeflate":
			writer, err = flate.NewWriter(&buffer, flate.DefaultCompression)
		case "br":
			writer = brotli.NewWriter(&buffer)
		case "zstd":
			writer, err = zstd.NewWriter(&buffer)
		default:
			t.Fatalf("unsupported test encoding: %s", encoding)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			writer.Close()
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		body = buffer.Bytes()
	}
	return body
}

func TestPassthroughErrorCompressedBodies(t *testing.T) {
	cases := []struct {
		name      string
		encodings []string
		headers   []string
	}{
		{name: "gzip", encodings: []string{"gzip"}, headers: []string{"gzip"}},
		{name: "zlib", encodings: []string{"deflate"}, headers: []string{"deflate"}},
		{name: "rawdeflate", encodings: []string{"rawdeflate"}, headers: []string{"deflate"}},
		{name: "brotli", encodings: []string{"br"}, headers: []string{"br"}},
		{name: "zstd", encodings: []string{"zstd"}, headers: []string{"zstd"}},
		{name: "stacked", encodings: []string{"gzip", "br"}, headers: []string{"gzip, br"}},
		{name: "multiple_headers", encodings: []string{"deflate", "gzip", "zstd"}, headers: []string{" Deflate , GZip ", " ZSTD "}},
		{name: "identity", encodings: []string{"gzip"}, headers: []string{"identity, gzip, identity"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, containsSecret := range []bool{false, true} {
				name := "no_echo"
				plain := []byte(`{"error":"upstream failed"}`)
				want := plain
				if containsSecret {
					name = "redacted"
					plain = []byte(`{"error":"invalid key: abc abc"}`)
					want = []byte(`{"error":"invalid key: [REDACTED] [REDACTED]"}`)
				}
				t.Run(name, func(t *testing.T) {
					body := encodePassthroughTestBody(t, plain, tc.encodings...)
					response := &http.Response{
						StatusCode: http.StatusUnprocessableEntity,
						Header: http.Header{
							"Content-Type":     {"application/json"},
							"Content-Encoding": append([]string(nil), tc.headers...),
							"Content-Length":   {strconv.Itoa(len(body))},
							"X-Error":          {"invalid abc", "another abc"},
						},
						ContentLength: int64(len(body)),
					}
					actual := redactPassthroughError(response, body, "abc")
					if !containsSecret {
						want = body
						if strings.Join(response.Header.Values("Content-Encoding"), "|") != strings.Join(tc.headers, "|") ||
							response.Header.Get("Content-Length") != strconv.Itoa(len(body)) || response.ContentLength != int64(len(body)) {
							t.Fatalf("original encoding/length changed: %v", response.Header)
						}
					} else if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Content-Length") != "" || response.ContentLength != -1 {
						t.Fatalf("stale encoding/length preserved: %v", response.Header)
					}
					if !bytes.Equal(actual, want) {
						t.Fatalf("body = %q, want %q", actual, want)
					}
					if response.StatusCode != http.StatusUnprocessableEntity || response.Header.Get("Content-Type") != "application/json" {
						t.Fatalf("status/type changed: %d, %v", response.StatusCode, response.Header)
					}
					if got := response.Header.Values("X-Error"); len(got) != 2 || got[0] != "invalid [REDACTED]" || got[1] != "another [REDACTED]" {
						t.Fatalf("business header leaked: %v", got)
					}
				})
			}
		})
	}
}

func TestPassthroughErrorDecodeFailureIsSafe(t *testing.T) {
	secret := "abc"
	plain := []byte("invalid key: " + secret)
	gzipBody := encodePassthroughTestBody(t, plain, "gzip")
	zlibBody := encodePassthroughTestBody(t, plain, "deflate")
	badGzipChecksum := append([]byte(nil), gzipBody...)
	badGzipChecksum[len(badGzipChecksum)-8] ^= 0xff
	badZlibChecksum := append([]byte(nil), zlibBody...)
	badZlibChecksum[len(badZlibChecksum)-1] ^= 0xff
	cases := []struct {
		name     string
		encoding string
		body     []byte
	}{
		{name: "unknown", encoding: "unknown", body: gzipBody},
		{name: "unknown_plaintext", encoding: "unknown", body: plain},
		{name: "unknown_inner", encoding: "unknown, gzip", body: gzipBody},
		{name: "invalid_gzip", encoding: "gzip", body: plain},
		{name: "gzip_checksum", encoding: "gzip", body: badGzipChecksum},
		{name: "truncated_gzip", encoding: "gzip", body: gzipBody[:len(gzipBody)-1]},
		{name: "gzip_is_not_deflate", encoding: "deflate", body: gzipBody},
		{name: "invalid_deflate", encoding: "deflate", body: []byte{0xff, 0xff}},
		{name: "zlib_checksum", encoding: "deflate", body: badZlibChecksum},
		{name: "truncated_zlib", encoding: "deflate", body: zlibBody[:len(zlibBody)-1]},
		{name: "invalid_brotli", encoding: "br", body: []byte{0xff, 0xff}},
		{name: "invalid_zstd", encoding: "zstd", body: plain},
		{name: "invalid_inner", encoding: "gzip, br", body: encodePassthroughTestBody(t, plain, "br")},
		{name: "empty_gzip", encoding: "gzip"},
		{name: "empty_token", encoding: "gzip,", body: gzipBody},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			response := &http.Response{
				StatusCode: http.StatusTeapot,
				Header: http.Header{
					"Content-Encoding": {tc.encoding},
					"Content-Length":   {strconv.Itoa(len(tc.body))},
					"Content-Type":     {"application/json"},
					"X-Error":          {"invalid abc"},
				},
				ContentLength: int64(len(tc.body)),
			}
			actual := redactPassthroughError(response, tc.body, secret)
			if string(actual) != "Upstream error response could not be safely decoded." || bytes.Contains(actual, []byte(secret)) {
				t.Fatalf("unsafe fallback: %q", actual)
			}
			if response.Header.Get("Content-Encoding") != "" || response.Header.Get("Content-Length") != "" || response.ContentLength != -1 {
				t.Fatalf("stale content headers: %v", response.Header)
			}
			if response.Header.Get("Content-Type") != "text/plain; charset=utf-8" || response.Header.Get("X-Error") != "invalid [REDACTED]" {
				t.Fatalf("wrong fallback headers: %v", response.Header)
			}
			response.Body = io.NopCloser(bytes.NewReader(actual))
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			(&ProxyServer{}).handlePassthroughResponse(c, response)
			if recorder.Code != http.StatusTeapot || !bytes.Equal(recorder.Body.Bytes(), actual) {
				t.Fatalf("fallback status/body changed: %d, %q", recorder.Code, recorder.Body.Bytes())
			}
		})
	}
}

func TestPassthroughErrorShortKeysPreserveProtocolHeaders(t *testing.T) {
	for _, secret := range []string{"json", "15", "gzip"} {
		for _, containsSecret := range []bool{false, true} {
			t.Run(secret+"/echo="+strconv.FormatBool(containsSecret), func(t *testing.T) {
				body := []byte("upstream failed")
				want := body
				if containsSecret {
					body = []byte("invalid key: " + secret)
					want = []byte("invalid key: [REDACTED]")
				}
				encoding := "identity"
				if secret == "gzip" {
					encoding = "gzip"
					body = encodePassthroughTestBody(t, body, "gzip")
				}
				length := strconv.Itoa(len(body))
				response := &http.Response{
					Header: http.Header{
						"Content-Type":     {"application/json"},
						"Content-Length":   {length},
						"Content-Encoding": {encoding},
						"Retry-After":      {"15"},
						"X-Error":          {"invalid " + secret},
						"Set-Cookie":       {"credential=" + secret},
					},
					ContentLength: int64(len(body)),
				}
				actual := redactPassthroughError(response, body, secret)
				if !containsSecret {
					want = body
					if response.Header.Get("Content-Length") != length || response.Header.Get("Content-Encoding") != encoding || response.ContentLength != int64(len(body)) {
						t.Fatalf("protocol headers corrupted: %v", response.Header)
					}
				} else if response.Header.Get("Content-Length") != "" || response.Header.Get("Content-Encoding") != "" || response.ContentLength != -1 {
					t.Fatalf("stale content headers: %v", response.Header)
				}
				if !bytes.Equal(actual, want) || response.Header.Get("Content-Type") != "application/json" || response.Header.Get("Retry-After") != "15" {
					t.Fatalf("body/type/metadata corrupted: %q, %v", actual, response.Header)
				}
				if response.Header.Get("X-Error") != "invalid [REDACTED]" || response.Header.Get("Set-Cookie") != "credential=[REDACTED]" {
					t.Fatalf("business header leaked: %v", response.Header)
				}
			})
		}
	}
}

func TestParamOverridesPreserveNonObjectBodies(t *testing.T) {
	group := &models.Group{ParamOverrides: datatypes.JSONMap{"limit": 1}}
	for _, body := range []string{"null", "[1,2]", "opaque", ""} {
		actual, err := (&ProxyServer{}).applyParamOverrides([]byte(body), group)
		if err != nil || string(actual) != body {
			t.Fatalf("body %q changed to %q: %v", body, actual, err)
		}
	}
}
