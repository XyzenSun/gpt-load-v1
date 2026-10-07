package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/failover"
	"gpt-load/internal/httpclient"
	"gpt-load/internal/keypool"
	"gpt-load/internal/models"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

const (
	otherTestKeyA = "local-only-secret-alpha"
	otherTestKeyB = "local-only-secret-beta"
)

type otherProxyFixture struct {
	proxy    *ProxyServer
	group    *models.Group
	channel  channel.ChannelProxy
	database *gorm.DB
	failures int64
}

func newOtherProxyFixture(t *testing.T, upstream string, retries int, failoverCodes string) *otherProxyFixture {
	t.Helper()
	// 使用独立 SQLite 和真实密钥池，失败路径的异步更新不能依赖空数据库。
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "keys.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	// 单连接让计数查询只能在更新事务提交之后读到结果。
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.APIKey{}); err != nil {
		t.Fatal(err)
	}
	memory := store.NewMemoryStore()
	t.Cleanup(func() { _ = memory.Close() })
	noopEncryption, err := encryption.NewService("")
	if err != nil {
		t.Fatal(err)
	}
	settings := config.NewSystemSettingsManager()
	group := &models.Group{
		ID: 1, Name: "other-test", GroupType: "standard", ChannelType: "other",
		Config: datatypes.JSONMap{
			"max_retries": retries, "failover_status_codes": failoverCodes,
			"request_timeout": 5, "connect_timeout": 2, "blacklist_threshold": 0,
		},
	}
	group.EffectiveConfig = settings.GetEffectiveConfig(group.Config)
	group.FailoverStatusCodeMatcher, err = failover.ParseStatusCodeMatcher(group.EffectiveConfig.FailoverStatusCodes)
	if err != nil {
		t.Fatal(err)
	}
	upstreams, err := json.Marshal([]map[string]any{{"url": upstream, "weight": 1}})
	if err != nil {
		t.Fatal(err)
	}
	group.Upstreams = datatypes.JSON(upstreams)
	provider := keypool.NewProvider(database, memory, settings, noopEncryption)
	// 内存 Store 的 Rotate 从列表尾部取值，按此顺序导入后首个密钥为 A。
	if err := provider.AddKeys(group.ID, []models.APIKey{
		{KeyValue: otherTestKeyB, KeyHash: noopEncryption.Hash(otherTestKeyB), GroupID: group.ID, Status: models.KeyStatusActive},
		{KeyValue: otherTestKeyA, KeyHash: noopEncryption.Hash(otherTestKeyA), GroupID: group.ID, Status: models.KeyStatusActive},
	}); err != nil {
		t.Fatal(err)
	}
	factory := channel.NewFactory(settings, httpclient.NewHTTPClientManager())
	handler, err := factory.GetChannel(group)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handler.GetHTTPClient().CloseIdleConnections()
		handler.GetStreamClient().CloseIdleConnections()
	})
	fixture := &otherProxyFixture{
		proxy: &ProxyServer{keyProvider: provider, encryptionSvc: noopEncryption},
		group: group, channel: handler, database: database,
	}
	// 清理顺序保证即使断言失败，也先等待失败计数落库，再关闭基础设施。
	t.Cleanup(func() { fixture.waitForFailures(t) })
	return fixture
}

func (f *otherProxyFixture) waitForFailures(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var keys []models.APIKey
		if err := f.database.Find(&keys).Error; err != nil {
			t.Error(err)
			return
		}
		var total int64
		for _, key := range keys {
			total += key.FailureCount
		}
		if total == f.failures {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("异步失败计数 = %d，期望 %d", total, f.failures)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *otherProxyFixture) execute(t *testing.T, req *http.Request, failures int64) *httptest.ResponseRecorder {
	t.Helper()
	f.failures += failures
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	_ = req.Body.Close()
	// 与 HandleProxy 相同，显式参数覆盖在进入重试流程之前执行。
	finalBody, err := f.proxy.applyParamOverrides(body, f.group)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = req
	isStream := f.channel.IsStreamRequest(c, body)
	if isStream {
		t.Fatal("other 不应根据请求内容推断流式模式")
	}
	f.proxy.executeRequestWithRetry(c, f.channel, f.group, f.group, finalBody, isStream, time.Now(), 0)
	// 直接调用核心流程时也需模拟 Gin 在 handler 返回后提交空响应状态码。
	c.Writer.WriteHeaderNow()
	f.waitForFailures(t)
	return recorder
}

type otherObservedRequest struct {
	method string
	uri    string
	header http.Header
	body   []byte
	err    error
}

func observeOtherRequest(r *http.Request) otherObservedRequest {
	body, err := io.ReadAll(r.Body)
	return otherObservedRequest{method: r.Method, uri: r.RequestURI, header: r.Header.Clone(), body: body, err: err}
}

func TestOtherProxyOAuthRotationAndOpaqueRequest(t *testing.T) {
	observed := make(chan otherObservedRequest, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- observeOtherRequest(r)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte{0, 0xff, 'o', 'k'})
	}))
	defer upstream.Close()
	f := newOtherProxyFixture(t, upstream.URL+"/base/root/", 0, "503")
	f.group.HeaderRuleList = []models.HeaderRule{{Key: "oauth", Value: "${API_KEY}", Action: "set"}}
	// 即使已有 AI 字段，通用渠道也不应解释非 JSON 请求或执行模型重定向。
	f.group.ModelRedirectStrict = true
	f.group.ModelRedirectMap = map[string]string{"source": "destination"}
	f.group.ParamOverrides = datatypes.JSONMap{"model": "explicit-but-not-json"}
	body := []byte("opaque=payload\x00\xff&stream=true")
	for i, key := range []string{otherTestKeyA, otherTestKeyB, otherTestKeyA} {
		req := httptest.NewRequest(http.MethodPatch, "/proxy/other-test/files/a%2Fb?x=1&x=2&q=a%2Bb", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer original-client-auth")
		req.Header.Set("X-Api-Key", "original-api-key")
		req.Header.Set("X-Goog-Api-Key", "original-google-key")
		req.Header.Set("oauth", "must-be-overridden")
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Connection", "X-Connection-Only")
		req.Header.Set("X-Connection-Only", "must-not-forward")
		got := f.execute(t, req, 0)
		if got.Code != http.StatusCreated || !bytes.Equal(got.Body.Bytes(), []byte{0, 0xff, 'o', 'k'}) {
			t.Fatalf("请求 %d 响应被改写：status=%d body=%q", i, got.Code, got.Body.Bytes())
		}
		seen := <-observed
		if seen.err != nil || seen.method != http.MethodPatch || seen.uri != "/base/root/files/a%2Fb?x=1&x=2&q=a%2Bb" || !bytes.Equal(seen.body, body) {
			t.Fatalf("请求 %d 未原样透传：%+v", i, seen)
		}
		for name, want := range map[string]string{
			"Authorization": "Bearer original-client-auth", "X-Api-Key": "original-api-key",
			"X-Goog-Api-Key": "original-google-key", "oauth": key, "Content-Type": "application/octet-stream",
		} {
			if value := seen.header.Get(name); value != want {
				t.Errorf("请求 %d 的 %s = %q，期望 %q", i, name, value, want)
			}
		}
		if seen.header.Get("Connection") != "" || seen.header.Get("X-Connection-Only") != "" {
			t.Errorf("连接级请求头不应透传：%v", seen.header)
		}
	}
}

func TestOtherProxyModelsResponseIsNotRebuilt(t *testing.T) {
	body := []byte("<models>not an AI model list</models>\n")
	observed := make(chan otherObservedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- observeOtherRequest(r)
		w.Header().Set("Content-Type", "application/xml")
		w.Header().Set("X-Upstream", "models")
		w.Header().Add("Set-Cookie", "first=1; Path=/")
		w.Header().Add("Set-Cookie", "second=2; HttpOnly")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()
	f := newOtherProxyFixture(t, upstream.URL+"/api", 0, "503")
	f.group.ModelRedirectStrict = true
	f.group.ModelRedirectMap = map[string]string{"alias": "upstream-model"}
	got := f.execute(t, httptest.NewRequest(http.MethodGet, "/proxy/other-test/v1/models?format=xml", nil), 0)
	seen := <-observed
	if seen.err != nil || seen.method != http.MethodGet || seen.uri != "/api/v1/models?format=xml" {
		t.Fatalf("模型列表请求不正确：%+v", seen)
	}
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), body) || got.Header().Get("Content-Type") != "application/xml" || got.Header().Get("X-Upstream") != "models" {
		t.Fatalf("非 JSON 模型列表被重建：status=%d headers=%v body=%q", got.Code, got.Header(), got.Body.Bytes())
	}
	if want := []string{"first=1; Path=/", "second=2; HttpOnly"}; !reflect.DeepEqual(got.Header().Values("Set-Cookie"), want) {
		t.Fatalf("多 Set-Cookie 丢失：%v", got.Header().Values("Set-Cookie"))
	}
}

func TestOtherProxyDoesNotInferSSEResponse(t *testing.T) {
	for _, tc := range []struct {
		name        string
		accept      string
		contentType string
		response    string
	}{
		{"sse_accept_json", "application/json", "text/event-stream; charset=utf-8", "event: custom\nid: 7\ndata: not-json\n\n: heartbeat\n\n"},
		{"sse_accept_any", "*/*", "text/event-stream", "data: [1,2]\n\ndata: [DONE]\n\n"},
		{"plain_accept_sse", "text/event-stream", "text/plain; charset=utf-8", "ordinary response, not SSE\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan otherObservedRequest, 1)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observed <- observeOtherRequest(r)
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Cache-Control", "private, max-age=37")
				w.Header().Set("X-Accel-Buffering", "upstream-choice")
				w.WriteHeader(http.StatusAccepted)
				// 分块发送，响应应增量回传但不能重新包装 SSE 事件。
				mid := len(tc.response) / 2
				_, _ = io.WriteString(w, tc.response[:mid])
				w.(http.Flusher).Flush()
				_, _ = io.WriteString(w, tc.response[mid:])
			}))
			defer upstream.Close()
			f := newOtherProxyFixture(t, upstream.URL, 0, "503")
			req := httptest.NewRequest(http.MethodPost, "/proxy/other-test/events", strings.NewReader(`{"stream":true,"model":"opaque"}`))
			req.Header.Set("Accept", tc.accept)
			got := f.execute(t, req, 0)
			seen := <-observed
			if seen.err != nil || seen.header.Get("Accept") != tc.accept || seen.header.Get("X-Accel-Buffering") != "" || string(seen.body) != `{"stream":true,"model":"opaque"}` {
				t.Fatalf("stream/Accept 导致请求被隐式修改：%+v", seen)
			}
			if got.Code != http.StatusAccepted || got.Body.String() != tc.response || !got.Flushed {
				t.Fatalf("响应未原样增量回传：status=%d flushed=%v body=%q", got.Code, got.Flushed, got.Body.String())
			}
			for name, want := range map[string]string{"Content-Type": tc.contentType, "Cache-Control": "private, max-age=37", "X-Accel-Buffering": "upstream-choice"} {
				if value := got.Header().Get(name); value != want {
					t.Errorf("响应头 %s = %q，期望 %q", name, value, want)
				}
			}
		})
	}
}

func TestOtherProxyExplicitParamOverrides(t *testing.T) {
	observed := make(chan otherObservedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- observeOtherRequest(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	f := newOtherProxyFixture(t, upstream.URL, 0, "503")
	f.group.ParamOverrides = datatypes.JSONMap{"model": "configured", "stream": false, "limit": 9}
	f.group.ModelRedirectStrict = true
	f.group.ModelRedirectMap = map[string]string{"configured": "must-not-redirect"}
	req := httptest.NewRequest(http.MethodPost, "/proxy/other-test/resource", strings.NewReader(`{"model":"client","stream":true,"limit":1,"keep":{"nested":true}}`))
	got := f.execute(t, req, 0)
	seen := <-observed
	var body map[string]any
	if seen.err != nil {
		t.Fatal(seen.err)
	}
	if err := json.Unmarshal(seen.body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"model": "configured", "stream": false, "limit": float64(9), "keep": map[string]any{"nested": true}}
	if got.Code != http.StatusNoContent || !reflect.DeepEqual(body, want) {
		t.Fatalf("显式参数覆盖不正确：status=%d body=%#v", got.Code, body)
	}
}

func TestOtherProxyFinalFailoverErrorPassthrough(t *testing.T) {
	for _, tc := range []struct {
		name        string
		contentType string
		body        []byte
		gzip        bool
	}{
		{"text", "text/plain; charset=utf-8", []byte("upstream unavailable\n"), false},
		{"json_array", "application/json", []byte(`[{"error":"unavailable"},7]`), false},
		{"binary", "application/octet-stream", []byte{0, 0xff, 0xfe, 1, 2}, false},
		{"text_secret", "text/plain", []byte("denied: " + otherTestKeyA + "\n"), false},
		{"json_array_secret", "application/json", []byte(`["` + otherTestKeyA + `",{"error":"denied"}]`), false},
		{"binary_secret", "application/octet-stream", append(append([]byte{0, 0xff}, []byte(otherTestKeyA)...), 0xfe), false},
		{"gzip_secret", "text/plain", []byte("compressed secret: " + otherTestKeyA), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wireBody := tc.body
			if tc.gzip {
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(tc.body); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				wireBody = compressed.Bytes()
			}
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts.Add(1)
				w.Header().Set("Content-Type", tc.contentType)
				w.Header().Set("Content-Length", fmt.Sprint(len(wireBody)))
				w.Header().Set("X-Upstream-Error", "failed")
				w.Header().Set("X-Error-Detail", "key="+otherTestKeyA)
				w.Header().Set("Retry-After", "17")
				w.Header().Add("Set-Cookie", "a=1")
				w.Header().Add("Set-Cookie", "b=2")
				w.Header().Set("Connection", "X-Hop-Only")
				w.Header().Set("X-Hop-Only", "must-not-forward")
				if tc.gzip {
					w.Header().Set("Content-Encoding", "gzip")
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write(wireBody)
			}))
			defer upstream.Close()
			f := newOtherProxyFixture(t, upstream.URL, 0, "503")
			req := httptest.NewRequest(http.MethodPost, "/proxy/other-test/action", strings.NewReader("opaque request"))
			// 明确请求 gzip，避免 HTTP Transport 自动解压掩盖代理脱敏逻辑。
			req.Header.Set("Accept-Encoding", "gzip")
			got := f.execute(t, req, 1)
			wantBody := bytes.ReplaceAll(tc.body, []byte(otherTestKeyA), []byte("[REDACTED]"))
			if got.Code != http.StatusServiceUnavailable || !bytes.Equal(got.Body.Bytes(), wantBody) || attempts.Load() != 1 {
				t.Fatalf("最终错误没有保留上游格式或 max_retries=0 重复请求：status=%d attempts=%d body=%q want=%q", got.Code, attempts.Load(), got.Body.Bytes(), wantBody)
			}
			for name, want := range map[string]string{"Content-Type": tc.contentType, "Retry-After": "17", "X-Upstream-Error": "failed", "X-Error-Detail": "key=[REDACTED]"} {
				if value := got.Header().Get(name); value != want {
					t.Errorf("错误响应头 %s = %q，期望 %q", name, value, want)
				}
			}
			if !reflect.DeepEqual(got.Header().Values("Set-Cookie"), []string{"a=1", "b=2"}) || got.Header().Get("X-Hop-Only") != "" || got.Header().Get("Connection") != "" {
				t.Errorf("错误响应多值头或连接级头处理不正确：%v", got.Header())
			}
			if bytes.Contains(tc.body, []byte(otherTestKeyA)) {
				if got.Header().Get("Content-Encoding") != "" || got.Header().Get("Content-Length") != "" {
					t.Errorf("脱敏后仍保留失效的编码/长度头：%v", got.Header())
				}
			} else if got.Header().Get("Content-Length") != fmt.Sprint(len(wireBody)) {
				t.Errorf("未改写错误体时丢失长度头：%v", got.Header())
			}
		})
	}
}

func TestOtherProxyPOSTRetryFollowsConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		succeedOnNext bool
		wantAttempts  int
		wantFailures  int64
		wantStatus    int
	}{
		{"matched_final_error", 503, false, 2, 2, 503},
		{"matched_then_success", 503, true, 2, 1, 201},
		{"unmatched_no_retry", 400, false, 1, 0, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan otherObservedRequest, 2)
			var attempts atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				observed <- observeOtherRequest(r)
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("X-Attempt", fmt.Sprint(attempt))
				if tc.succeedOnNext && attempt == 2 {
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, "created")
					return
				}
				w.WriteHeader(tc.status)
				if tc.wantFailures == 0 {
					_, _ = io.WriteString(w, "ordinary bad request")
					return
				}
				_, _ = io.WriteString(w, "denied: "+r.Header.Get("oauth"))
			}))
			defer upstream.Close()
			f := newOtherProxyFixture(t, upstream.URL+"/base", 1, "503")
			f.group.HeaderRuleList = []models.HeaderRule{{Key: "oauth", Value: "${API_KEY}", Action: "set"}}
			req := httptest.NewRequest(http.MethodPost, "/proxy/other-test/action?commit=1", strings.NewReader("side-effect payload"))
			req.Header.Set("Authorization", "Bearer original")
			got := f.execute(t, req, tc.wantFailures)
			if got.Code != tc.wantStatus || attempts.Load() != int32(tc.wantAttempts) || got.Header().Get("X-Attempt") != fmt.Sprint(tc.wantAttempts) {
				t.Fatalf("POST 重试不符合配置：status=%d attempts=%d headers=%v", got.Code, attempts.Load(), got.Header())
			}
			for i := 0; i < tc.wantAttempts; i++ {
				seen := <-observed
				wantKey := []string{otherTestKeyA, otherTestKeyB}[i]
				if seen.err != nil || seen.method != http.MethodPost || seen.uri != "/base/action?commit=1" || string(seen.body) != "side-effect payload" || seen.header.Get("oauth") != wantKey || seen.header.Get("Authorization") != "Bearer original" {
					t.Fatalf("重试 %d 没有复用请求并轮询密钥：%+v", i, seen)
				}
			}
			wantBody := "denied: [REDACTED]"
			if tc.succeedOnNext {
				wantBody = "created"
			} else if tc.wantFailures == 0 {
				// 未命中故障转移的状态码直接回传，不能仅因是 POST 错误就重试。
				wantBody = "ordinary bad request"
			}
			if got.Body.String() != wantBody {
				t.Fatalf("最终响应不是最后一次上游结果：%q，期望 %q", got.Body.String(), wantBody)
			}
		})
	}
}

func TestOtherProxyNetworkErrorUsesStandardErrorFormat(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		// 建立本地连接后直接断开，制造真实网络错误而不是 HTTP 错误响应。
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	defer upstream.Close()
	f := newOtherProxyFixture(t, upstream.URL, 0, "503")
	// 传输错误会带上完整 URL，验证其中的当前密钥同样不能暴露给客户端。
	req := httptest.NewRequest(http.MethodPost, "/proxy/other-test/action?token="+otherTestKeyA, strings.NewReader("payload"))
	got := f.execute(t, req, 1)
	var body struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &body); err != nil {
		t.Fatalf("网络错误没有使用标准 JSON 格式：%v，body=%q", err, got.Body.String())
	}
	if got.Code != http.StatusInternalServerError || attempts.Load() != 1 || body.Code != "UPSTREAM_ERROR" || body.Message == "" || !strings.HasPrefix(got.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("网络错误不正确：status=%d attempts=%d headers=%v body=%+v", got.Code, attempts.Load(), got.Header(), body)
	}
	// 网络错误沿用统一 RedactSecret 掩码，不要求与透传错误的占位符相同。
	if strings.Contains(got.Body.String(), otherTestKeyA) || !strings.Contains(body.Message, "****") {
		t.Fatalf("网络错误中的密钥未脱敏：%q", got.Body.String())
	}
}
