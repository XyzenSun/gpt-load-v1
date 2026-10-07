package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"gpt-load/internal/config"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n"
	"gpt-load/internal/i18n/locales"
	"gpt-load/internal/models"
	"gpt-load/internal/response"
	"gpt-load/internal/services"
	"gpt-load/internal/store"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func TestGroupUpdateRequestTestModelBinding(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		present bool
		value   string
		invalid bool
	}{
		{name: "omitted", body: `{}`},
		{name: "null", body: `{"test_model":null}`},
		{name: "empty", body: `{"test_model":""}`, present: true},
		{name: "whitespace", body: `{"test_model":" \t "}`, present: true, value: " \t "},
		{name: "model", body: `{"test_model":" model "}`, present: true, value: " model "},
		{name: "number", body: `{"test_model":123}`, invalid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(tc.body))
			c.Request.Header.Set("Content-Type", "application/json")
			var req GroupUpdateRequest
			err := c.ShouldBindJSON(&req)
			if (err != nil) != tc.invalid {
				t.Fatalf("binding error = %v, want invalid=%v", err, tc.invalid)
			}
			if tc.invalid {
				return
			}
			if (req.TestModel != nil) != tc.present {
				t.Fatalf("test_model = %v, want present=%v", req.TestModel, tc.present)
			}
			if tc.present && *req.TestModel != tc.value {
				t.Fatalf("test_model = %q, want %q", *req.TestModel, tc.value)
			}
		})
	}
}

// 不广播缓存失效, 直接验证持久化结果, 避免异步重载影响测试.
// 缓存保持为空, 确保目标渠道判断不能依赖可能过期的缓存.
type groupHandlerTestStore struct {
	*store.MemoryStore
}

func (*groupHandlerTestStore) Publish(_ string, _ []byte) error { return nil }

func newGroupHandlerTestServer(t *testing.T) (*Server, *gin.Engine) {
	t.Helper()
	if err := i18n.Init(); err != nil {
		t.Fatal(err)
	}
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "groups.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.AutoMigrate(&models.Group{}, &models.GroupSubGroup{}); err != nil {
		t.Fatal(err)
	}
	settings := config.NewSystemSettingsManager()
	memory := &groupHandlerTestStore{MemoryStore: store.NewMemoryStore()}
	manager := services.NewGroupManager(database, memory, settings, services.NewSubGroupManager(memory), nil)
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Stop(context.Background()) })
	aggregate := services.NewAggregateGroupService(database, manager)
	server := &Server{
		DB: database, SettingsManager: settings,
		GroupService: services.NewGroupService(database, settings, manager, nil, nil, nil, aggregate),
	}
	router := gin.New()
	router.Use(i18n.Middleware())
	router.PUT("/groups/:id", server.UpdateGroup)
	return server, router
}

func updateGroupHTTP(router *gin.Engine, id uint, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/groups/%d", id), strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Language", "en-US")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func TestUpdateGroupTestModelHTTP(t *testing.T) {
	server, router := newGroupHandlerTestServer(t)
	for _, tc := range []struct {
		name         string
		channel      string
		model        string
		body         string
		wantChannel  string
		wantModel    string
		wantError    string
		wantMetadata bool
	}{
		{name: "other_clear_existing", channel: "other", model: "stored-model", body: `{"test_model":""}`, wantChannel: "other"},
		{name: "other_clear_explicit_channel", channel: "other", model: "stored-model", body: `{"channel_type":"other","test_model":""}`, wantChannel: "other"},
		{name: "other_omitted", channel: "other", model: "stored-model", body: `{}`, wantChannel: "other", wantModel: "stored-model"},
		{name: "other_null", channel: "other", model: "stored-model", body: `{"test_model":null}`, wantChannel: "other", wantModel: "stored-model"},
		{name: "other_whitespace", channel: "other", model: "stored-model", body: `{"test_model":" \t "}`, wantChannel: "other"},
		{name: "other_nonempty", channel: "other", model: "stored-model", body: `{"test_model":" new-model "}`, wantChannel: "other", wantModel: "new-model"},
		{name: "ai_empty_ignored", channel: "openai", model: "stored-model", body: `{"test_model":""}`, wantChannel: "openai", wantModel: "stored-model"},
		{name: "ai_explicit_empty_ignored", channel: "openai", model: "stored-model", body: `{"channel_type":"openai","test_model":""}`, wantChannel: "openai", wantModel: "stored-model"},
		{name: "ai_omitted", channel: "openai", model: "stored-model", body: `{}`, wantChannel: "openai", wantModel: "stored-model"},
		{name: "ai_null", channel: "openai", model: "stored-model", body: `{"test_model":null}`, wantChannel: "openai", wantModel: "stored-model"},
		{name: "ai_whitespace_rejected", channel: "openai", model: "stored-model", body: `{"test_model":" \t "}`, wantError: "validation.test_model_empty"},
		{name: "ai_nonempty", channel: "openai", model: "stored-model", body: `{"test_model":" new-model "}`, wantChannel: "openai", wantModel: "new-model"},
		{name: "gemini_empty_ignored", channel: "gemini", model: "stored-model", body: `{"test_model":""}`, wantChannel: "gemini", wantModel: "stored-model"},
		{name: "anthropic_empty_ignored", channel: "anthropic", model: "stored-model", body: `{"test_model":""}`, wantChannel: "anthropic", wantModel: "stored-model"},
		{name: "openai_response_empty_ignored", channel: "openai-response", model: "stored-model", body: `{"test_model":""}`, wantChannel: "openai-response", wantModel: "stored-model"},
		{name: "ai_to_other_clear", channel: "openai", model: "stored-model", body: `{"channel_type":"other","test_model":""}`, wantChannel: "other"},
		{name: "ai_to_other_trimmed_channel", channel: "openai", model: "stored-model", body: `{"channel_type":" other ","test_model":""}`, wantChannel: "other"},
		{name: "ai_to_other_omitted", channel: "openai", model: "stored-model", body: `{"channel_type":"other"}`, wantChannel: "other", wantModel: "stored-model"},
		{name: "other_to_ai_empty_ignored", channel: "other", model: "stored-model", body: `{"channel_type":"openai","test_model":""}`, wantChannel: "openai", wantModel: "stored-model"},
		{name: "other_to_ai_requires_model_omitted", channel: "other", body: `{"channel_type":"openai"}`, wantError: "validation.test_model_required"},
		{name: "other_to_ai_requires_model_empty", channel: "other", body: `{"channel_type":"openai","test_model":""}`, wantError: "validation.test_model_required"},
		{name: "other_to_ai_whitespace_rejected", channel: "other", body: `{"channel_type":"openai","test_model":" \t "}`, wantError: "validation.test_model_empty"},
		{name: "other_to_ai_nonempty", channel: "other", body: `{"channel_type":"openai","test_model":" new-model "}`, wantChannel: "openai", wantModel: "new-model"},
		{name: "other_clear_with_metadata", channel: "other", model: "stored-model", body: `{"test_model":"","display_name":" updated ","description":" updated ","sort":0,"model_redirect_strict":false}`, wantChannel: "other", wantMetadata: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := models.Group{
				Name: tc.name, GroupType: "standard", ChannelType: tc.channel, TestModel: tc.model,
				DisplayName: "original", Description: "original", Sort: 7, ModelRedirectStrict: true,
				Upstreams:          datatypes.JSON(`[{"url":"https://example.com","weight":1}]`),
				ValidationEndpoint: "/retained/endpoint",
				ParamOverrides:     datatypes.JSONMap{"model": "retained-override"},
				ModelRedirectRules: datatypes.JSONMap{"source": "destination"},
			}
			if err := server.DB.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			recorder := updateGroupHTTP(router, group.ID, tc.body)
			var stored models.Group
			if err := server.DB.First(&stored, group.ID).Error; err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if recorder.Code != app_errors.ErrValidation.HTTPStatus {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				var result response.ErrorResponse
				if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Code != app_errors.ErrValidation.Code || result.Message != locales.MessagesEnUS[tc.wantError] {
					t.Fatalf("error=%+v, want %s", result, tc.wantError)
				}
				if stored.ChannelType != tc.channel || stored.TestModel != tc.model {
					t.Fatalf("rejected update changed persisted channel/model: %q/%q", stored.ChannelType, stored.TestModel)
				}
			} else {
				if recorder.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
				}
				var result struct {
					Code int           `json:"code"`
					Data GroupResponse `json:"data"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if result.Code != 0 || result.Data.ID != group.ID || result.Data.ChannelType != tc.wantChannel || result.Data.TestModel != tc.wantModel {
					t.Fatalf("unexpected response: %s", recorder.Body.String())
				}
				if stored.ChannelType != tc.wantChannel || stored.TestModel != tc.wantModel {
					t.Fatalf("persisted channel/model=%q/%q, want %q/%q", stored.ChannelType, stored.TestModel, tc.wantChannel, tc.wantModel)
				}
			}
			if stored.ValidationEndpoint != group.ValidationEndpoint || string(stored.Upstreams) != string(group.Upstreams) ||
				stored.ParamOverrides["model"] != "retained-override" || stored.ModelRedirectRules["source"] != "destination" {
				t.Fatalf("unrelated fields changed: %+v", stored)
			}
			if tc.wantMetadata {
				if stored.DisplayName != "updated" || stored.Description != "updated" || stored.Sort != 0 || stored.ModelRedirectStrict {
					t.Fatalf("metadata update changed behavior: %+v", stored)
				}
			} else if stored.DisplayName != group.DisplayName || stored.Description != group.Description || stored.Sort != group.Sort || !stored.ModelRedirectStrict {
				t.Fatalf("omitted metadata changed: %+v", stored)
			}
		})
	}
}

func TestUpdateGroupTestModelQueryCountHTTP(t *testing.T) {
	server, router := newGroupHandlerTestServer(t)
	groupQueries := 0
	if err := server.DB.Callback().Query().After("gorm:query").Register("test:count_group_queries", func(tx *gorm.DB) {
		if tx.Statement.Table == "groups" {
			groupQueries++
		}
	}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		channel     string
		body        string
		wantQueries int
		wantStatus  int
	}{
		{name: "omitted", channel: "openai", body: `{}`, wantQueries: 1, wantStatus: http.StatusOK},
		{name: "null", channel: "openai", body: `{"test_model":null}`, wantQueries: 1, wantStatus: http.StatusOK},
		{name: "ai_nonempty", channel: "openai", body: `{"test_model":"new-model"}`, wantQueries: 1, wantStatus: http.StatusOK},
		{name: "ai_whitespace", channel: "openai", body: `{"test_model":" \t "}`, wantQueries: 1, wantStatus: http.StatusBadRequest},
		{name: "other_nonempty", channel: "other", body: `{"test_model":"new-model"}`, wantQueries: 1, wantStatus: http.StatusOK},
		{name: "ai_empty_lookup", channel: "openai", body: `{"test_model":""}`, wantQueries: 2, wantStatus: http.StatusOK},
		{name: "other_empty_lookup", channel: "other", body: `{"test_model":""}`, wantQueries: 2, wantStatus: http.StatusOK},
		{name: "explicit_other_no_lookup", channel: "openai", body: `{"channel_type":"other","test_model":""}`, wantQueries: 1, wantStatus: http.StatusOK},
		{name: "explicit_ai_no_lookup", channel: "other", body: `{"channel_type":"openai","test_model":""}`, wantQueries: 1, wantStatus: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			group := models.Group{Name: tc.name, GroupType: "standard", ChannelType: tc.channel, TestModel: "stored-model", Upstreams: datatypes.JSON("[]")}
			if err := server.DB.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			groupQueries = 0
			recorder := updateGroupHTTP(router, group.ID, tc.body)
			if recorder.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			// service 自带一次分组查询, 仅显式空模型且未提供渠道时多查一次.
			if groupQueries != tc.wantQueries {
				t.Fatalf("group queries=%d, want %d", groupQueries, tc.wantQueries)
			}
		})
	}
}

func TestUpdateGroupTestModelMissingGroupHTTP(t *testing.T) {
	_, router := newGroupHandlerTestServer(t)
	for _, body := range []string{`{"test_model":""}`, `{"test_model":"model"}`, `{"channel_type":"other","test_model":""}`} {
		recorder := updateGroupHTTP(router, 999, body)
		var result response.ErrorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != app_errors.ErrResourceNotFound.HTTPStatus || result.Code != app_errors.ErrResourceNotFound.Code {
			t.Fatalf("body=%s status=%d response=%s", body, recorder.Code, recorder.Body.String())
		}
	}
}
