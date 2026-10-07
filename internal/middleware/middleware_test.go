package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"gpt-load/internal/config"
	"gpt-load/internal/models"
	"gpt-load/internal/services"
	"gpt-load/internal/types"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func authTestContext(rawQuery string, headers map[string]string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/proxy", nil)
	c.Request.URL.RawQuery = rawQuery
	for name, value := range headers {
		c.Request.Header.Set(name, value)
	}
	return c
}

func TestExtractAuthKeyCompatibility(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		headers map[string]string
		key     string
		wantRaw string
	}{
		{"query first and delete all", "key=query&x=1&key=business", map[string]string{"Authorization": "Bearer header"}, "query", "x=1"},
		{"bearer first", "x=%2f", map[string]string{"Authorization": "Bearer bearer", "X-Api-Key": "api", "X-Goog-Api-Key": "google"}, "bearer", "x=%2f"},
		{"api key", "", map[string]string{"X-Api-Key": "api", "X-Goog-Api-Key": "google"}, "api", ""},
		{"google key", "", map[string]string{"X-Goog-Api-Key": "google"}, "google", ""},
		{"non bearer falls through", "", map[string]string{"Authorization": "Basic ignored", "X-Api-Key": "api"}, "api", ""},
		{"bearer prefix remains case sensitive", "", map[string]string{"Authorization": "bearer ignored", "X-Goog-Api-Key": "google"}, "google", ""},
		{"bearer content not trimmed", "", map[string]string{"Authorization": "Bearer  token "}, " token ", ""},
		{"empty bearer retains legacy behavior", "", map[string]string{"Authorization": "Bearer ", "X-Api-Key": "api"}, "", ""},
		{"empty query not removed", "key=&x=%2f", map[string]string{"X-Api-Key": "api"}, "api", "key=&x=%2f"},
		{"missing", "key=&x=1", nil, "", "key=&x=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := authTestContext(tt.query, tt.headers)
			if got := extractAuthKey(c); got != tt.key {
				t.Fatalf("key = %q, want %q", got, tt.key)
			}
			if got := c.Request.URL.RawQuery; got != tt.wantRaw {
				t.Fatalf("RawQuery = %q, want %q", got, tt.wantRaw)
			}
		})
	}
}

func newAuthTestGroupManager(t *testing.T) *services.GroupManager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.Group{}, &models.GroupSubGroup{}); err != nil {
		t.Fatal(err)
	}
	for _, spec := range []struct{ name, channel, groupType string }{
		{"other", "other", "standard"},
		{"aggregate", "other", "aggregate"},
		{"openai", "openai", "standard"},
		{"gemini", "gemini", "standard"},
		{"anthropic", "anthropic", "standard"},
		{"openai-response", "openai-response", "standard"},
	} {
		group := models.Group{
			Name: spec.name, ChannelType: spec.channel, GroupType: spec.groupType,
			ProxyKeys: "proxy", Upstreams: datatypes.JSON("[]"), TestModel: "test",
		}
		if err := db.Create(&group).Error; err != nil {
			t.Fatal(err)
		}
	}
	gm := services.NewGroupManager(db, nil, config.NewSystemSettingsManager(), services.NewSubGroupManager(nil))
	if err := gm.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { gm.Stop(context.Background()) })
	return gm
}

func TestProxyAuthCredentialPrecedence(t *testing.T) {
	gm := newAuthTestGroupManager(t)
	const businessQuery = "z=%2f&key=business&key=second&space=a%20b&empty=&flag"
	tests := []struct {
		name, group, query string
		headers            map[string]string
		status             int
		wantRaw            string
	}{
		{"other bearer preserves raw query", "other", businessQuery, map[string]string{"Authorization": "Bearer proxy"}, 204, businessQuery},
		{"other api key preserves raw query", "other", businessQuery, map[string]string{"X-Api-Key": "proxy"}, 204, businessQuery},
		{"other google key preserves raw query", "other", businessQuery, map[string]string{"X-Goog-Api-Key": "proxy"}, 204, businessQuery},
		{"other header wins over valid query", "other", "key=proxy&x=%2f", map[string]string{"Authorization": "Bearer wrong"}, 401, "key=proxy&x=%2f"},
		{"other bearer wins over other headers", "other", businessQuery, map[string]string{"Authorization": "Bearer wrong", "X-Api-Key": "proxy"}, 401, businessQuery},
		{"other api wins over google", "other", businessQuery, map[string]string{"X-Api-Key": "wrong", "X-Goog-Api-Key": "proxy"}, 401, businessQuery},
		{"other query fallback removes every key", "other", "key=proxy&x=1&key=business&%6Bey=third", nil, 204, "x=1"},
		{"other non bearer query fallback", "other", "key=proxy&key=business&x=1", map[string]string{"Authorization": "Basic ignored"}, 204, "x=1"},
		{"other empty bearer query fallback", "other", "key=proxy&key=business", map[string]string{"Authorization": "Bearer "}, 204, ""},
		{"other empty bearer usable api header", "other", businessQuery, map[string]string{"Authorization": "Bearer ", "X-Api-Key": "proxy"}, 204, businessQuery},
		{"other empty bearer usable google header", "other", businessQuery, map[string]string{"Authorization": "Bearer ", "X-Goog-Api-Key": "proxy"}, 204, businessQuery},
		{"other bad query consumed on failure", "other", "key=wrong&key=proxy&x=1", nil, 401, "x=1"},
		{"other no credential leaves query", "other", "key=&x=%2f", nil, 401, "key=&x=%2f"},
		{"aggregate stays query first", "aggregate", "key=proxy&key=business&x=1", map[string]string{"Authorization": "Bearer wrong"}, 204, "x=1"},
	}
	for _, name := range []string{"openai", "gemini", "anthropic", "openai-response"} {
		tests = append(tests, struct {
			name, group, query string
			headers            map[string]string
			status             int
			wantRaw            string
		}{name + " stays query first", name, "key=proxy&key=business&x=1", map[string]string{"Authorization": "Bearer wrong"}, 204, "x=1"})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := gin.New()
			router.GET("/proxy/:group_name", ProxyAuth(gm), func(c *gin.Context) { c.Status(http.StatusNoContent) })
			req := httptest.NewRequest(http.MethodGet, "/proxy/"+tt.group, nil)
			req.URL.RawQuery = tt.query
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.status, w.Body.String())
			}
			if got := req.URL.RawQuery; got != tt.wantRaw {
				t.Fatalf("RawQuery = %q, want %q", got, tt.wantRaw)
			}
		})
	}
}

func TestAuthRemainsQueryFirst(t *testing.T) {
	for _, tt := range []struct {
		query, header string
		status        int
	}{
		{"key=proxy&key=business&x=1", "Bearer wrong", 204},
		{"key=wrong&key=proxy&x=1", "Bearer proxy", 401},
	} {
		router := gin.New()
		router.GET("/admin", Auth(types.AuthConfig{Key: "proxy"}), func(c *gin.Context) { c.Status(http.StatusNoContent) })
		req := httptest.NewRequest(http.MethodGet, "/admin?"+tt.query, nil)
		req.Header.Set("Authorization", tt.header)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != tt.status || req.URL.RawQuery != "x=1" {
			t.Fatalf("status = %d, RawQuery = %q; want %d, x=1", w.Code, req.URL.RawQuery, tt.status)
		}
	}
}

func TestOtherAuthLegacyStandardGroup(t *testing.T) {
	c := authTestContext("key=business&x=%2f", map[string]string{"X-Api-Key": "proxy"})
	if key := extractAuthKeyForGroup(c, "other", ""); key != "proxy" || c.Request.URL.RawQuery != "key=business&x=%2f" {
		t.Fatalf("key = %q, RawQuery = %q", key, c.Request.URL.RawQuery)
	}
}
