package channel

import (
	"bytes"
	"context"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"
	"net/http"
	"net/url"
	"testing"

	"gorm.io/datatypes"
)

func TestOtherChannelBuildUpstreamURL(t *testing.T) {
	tests := []struct {
		name     string
		base     string
		request  string
		expected string
	}{
		{"append path", "https://api.abc.com/12345", "/proxy/search/123456?q=test", "https://api.abc.com/12345/123456?q=test"},
		{"replace query", "https://api.abc.com/base/?default=1", "/proxy/search/search?q=a%20b&key=business", "https://api.abc.com/base/search?q=a%20b&key=business"},
		{"clear base query", "https://api.abc.com?default=1", "/proxy/search/", "https://api.abc.com/"},
		{"encoded trailing slash", "https://api.abc.com/base%2F", "/proxy/search/files/a%2Fb", "https://api.abc.com/base%2F/files/a%2Fb"},
		{"encoded slashes", "https://api.abc.com/base%2Fpart", "/proxy/search/object%2Fname?q=%2F", "https://api.abc.com/base%2Fpart/object%2Fname?q=%2F"},
		{"encoded characters", "https://api.abc.com/base", "/proxy/search/%E6%90%9C%E7%B4%A2/%25", "https://api.abc.com/base/%E6%90%9C%E7%B4%A2/%25"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, err := url.Parse(test.base)
			if err != nil {
				t.Fatal(err)
			}
			original, err := url.Parse(test.request)
			if err != nil {
				t.Fatal(err)
			}
			ch := &OtherChannel{BaseChannel: &BaseChannel{Name: "other", Upstreams: []UpstreamInfo{{URL: base, Weight: 1}}}}
			actual, err := ch.BuildUpstreamURL(original, "search")
			if err != nil {
				t.Fatal(err)
			}
			if actual != test.expected {
				t.Fatalf("URL = %q, want %q", actual, test.expected)
			}
		})
	}
}

func TestOtherChannelIgnoresAIConfiguration(t *testing.T) {
	group := &models.Group{
		ChannelType:         "other",
		Upstreams:           datatypes.JSON(`[{"url":"https://api.example.com","weight":1}]`),
		TestModel:           "unused",
		ValidationEndpoint:  "/unused",
		ModelRedirectMap:    map[string]string{"original": "replacement"},
		ModelRedirectStrict: true,
	}
	factory := NewFactory(nil, httpclient.NewHTTPClientManager())
	ch, err := factory.GetChannel(group)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.IsPassthrough() {
		t.Fatal("other must use passthrough semantics")
	}
	body := []byte(`{"model":"original","stream":true}`)
	request, err := http.NewRequest("POST", "https://api.example.com/search", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer client-credential")
	request.Header.Set("X-Auth-Key", "client-upstream-key")
	ch.ModifyRequest(request, &models.APIKey{KeyValue: "pool-key"}, group)
	if request.Header.Get("Authorization") != "Bearer client-credential" || request.Header.Get("X-Auth-Key") != "client-upstream-key" {
		t.Fatal("other modified client credentials")
	}
	actual, err := ch.ApplyModelRedirect(request, body, group)
	if err != nil || !bytes.Equal(actual, body) {
		t.Fatalf("model mapping changed body: %q, %v", actual, err)
	}
	if ch.IsStreamRequest(nil, body) {
		t.Fatal("other must not infer SSE from JSON")
	}
	if ch.ExtractModel(nil, body) != "" {
		t.Fatal("other extracted AI model")
	}
	valid, err := ch.ValidateKey(context.Background(), nil, group)
	if err != nil || !valid {
		t.Fatalf("skip validation = %v, %v", valid, err)
	}
}
