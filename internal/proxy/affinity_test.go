package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/models"

	"github.com/gin-gonic/gin"
)

func newAffinityProxyFixture(t *testing.T, upstream, channelType string, retries int, failoverCodes string) *otherProxyFixture {
	t.Helper()
	fixture := newOtherProxyFixture(t, upstream, retries, failoverCodes)
	fixture.group.ChannelType = channelType
	fixture.group.EffectiveConfig.EnableKeyAffinity = true
	factory := channel.NewFactory(nil, httpclient.NewHTTPClientManager())
	handler, err := factory.GetChannel(fixture.group)
	if err != nil {
		t.Fatal(err)
	}
	fixture.channel = handler
	t.Cleanup(func() {
		handler.GetHTTPClient().CloseIdleConnections()
		handler.GetStreamClient().CloseIdleConnections()
	})
	return fixture
}

func affinityRequest() *http.Request {
	return httptest.NewRequest(http.MethodPost, "/proxy/other-test/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[]}`))
}

func TestProxyKeyAffinityReusesSuccessAndSwitchesOnFailure(t *testing.T) {
	for _, channelType := range []string{"openai", "anthropic"} {
		t.Run(channelType, func(t *testing.T) {
			observed := make(chan string, 8)
			var failKeyA atomic.Bool
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
				if channelType == "anthropic" {
					key = r.Header.Get("X-Api-Key")
				}
				observed <- key
				w.Header().Set("Content-Type", "application/json")
				if failKeyA.Load() && key == otherTestKeyA {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"ok":true}`))
			}))
			defer upstream.Close()
			fixture := newAffinityProxyFixture(t, upstream.URL, channelType, 1, "503")
			for range 2 {
				if got := fixture.execute(t, affinityRequest(), 0); got.Code != http.StatusOK {
					t.Fatalf("status = %d, body = %s", got.Code, got.Body.String())
				}
				if got := <-observed; got != otherTestKeyA {
					t.Fatalf("successful affinity key = %q, want A", got)
				}
			}
			failKeyA.Store(true)
			if got := fixture.execute(t, affinityRequest(), 1); got.Code != http.StatusOK {
				t.Fatalf("retry status = %d, body = %s", got.Code, got.Body.String())
			}
			if first, second := <-observed, <-observed; first != otherTestKeyA || second != otherTestKeyB {
				t.Fatalf("retry keys = %q, %q; want A, B", first, second)
			}
			fixture.execute(t, affinityRequest(), 0)
			if got := <-observed; got != otherTestKeyB {
				t.Fatalf("new affinity key = %q, want B", got)
			}
		})
	}
}

func TestProxyKeyAffinityNonRetryableErrorDoesNotBind(t *testing.T) {
	observed := make(chan string, 3)
	var status atomic.Int64
	status.Store(http.StatusBadRequest)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.WriteHeader(int(status.Load()))
		_, _ = w.Write([]byte(`{"result":"test"}`))
	}))
	defer upstream.Close()
	fixture := newAffinityProxyFixture(t, upstream.URL, "openai", 0, "503")
	if got := fixture.execute(t, affinityRequest(), 0); got.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", got.Code)
	}
	if got := <-observed; got != otherTestKeyA {
		t.Fatalf("first key = %q, want A", got)
	}
	status.Store(http.StatusOK)
	for range 2 {
		fixture.execute(t, affinityRequest(), 0)
		if got := <-observed; got != otherTestKeyB {
			t.Fatalf("key after non-success = %q, want B", got)
		}
	}
}

func TestProxyKeyAffinityOtherAlwaysRotates(t *testing.T) {
	observed := make(chan string, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Get("oauth")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	fixture := newOtherProxyFixture(t, upstream.URL, 0, "503")
	fixture.group.HeaderRuleList = []models.HeaderRule{{Key: "oauth", Value: "${API_KEY}", Action: "set"}}
	fixture.group.EffectiveConfig.EnableKeyAffinity = true
	for _, want := range []string{otherTestKeyA, otherTestKeyB, otherTestKeyA} {
		fixture.execute(t, affinityRequest(), 0)
		if got := <-observed; got != want {
			t.Fatalf("other key = %q, want %q", got, want)
		}
	}
}

func TestProxyKeyAffinityClientCancellationPreservesBinding(t *testing.T) {
	observed := make(chan string, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	fixture := newAffinityProxyFixture(t, upstream.URL, "openai", 1, "503")
	fixture.execute(t, affinityRequest(), 0)
	<-observed
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	fixture.execute(t, affinityRequest().WithContext(ctx), 0)
	fixture.execute(t, affinityRequest(), 0)
	if got := <-observed; got != otherTestKeyA {
		t.Fatalf("key after cancellation = %q, want A", got)
	}
}

func TestProxyKeyAffinityStreamingUsesExistingSuccessStandard(t *testing.T) {
	observed := make(chan string, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		w.Header().Set("Content-Type", "text/event-stream")
		// SSE 内容保持透传, HTTP 200 下的业务错误不改变现有成功标准.
		_, _ = w.Write([]byte("event: error\ndata: {\"error\":\"business error\"}\n\n"))
	}))
	defer upstream.Close()
	fixture := newAffinityProxyFixture(t, upstream.URL, "openai", 0, "503")
	for range 2 {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = affinityRequest()
		_ = c.Request.Body.Close()
		fixture.proxy.executeRequestWithRetry(c, fixture.channel, fixture.group, fixture.group,
			[]byte(`{"model":"test","stream":true}`), true, time.Now(), 0)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "business error") {
			t.Fatalf("stream response = %d, %s", recorder.Code, recorder.Body.String())
		}
		if got := <-observed; got != otherTestKeyA {
			t.Fatalf("stream affinity key = %q, want A", got)
		}
	}
}
