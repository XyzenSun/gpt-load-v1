package services

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"gpt-load/internal/config"
	app_errors "gpt-load/internal/errors"
	"gpt-load/internal/i18n/locales"
	"gpt-load/internal/models"
	"gpt-load/internal/store"
	"gpt-load/internal/utils"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 本组测试验证数据库持久化, 不依赖异步缓存重载.
// 使用无广播测试 store, 避免清理时触发现有 MemoryStore 发布与关闭的竞态.
type otherTestStore struct {
	*store.MemoryStore
}

func (s *otherTestStore) Publish(_ string, _ []byte) error { return nil }

func newOtherTestService(t *testing.T) *GroupService {
	t.Helper()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "groups.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := database.Exec("PRAGMA journal_mode=WAL").Error; err != nil {
		t.Fatal(err)
	}
	if err := database.AutoMigrate(&models.Group{}, &models.GroupSubGroup{}); err != nil {
		t.Fatal(err)
	}
	settings := config.NewSystemSettingsManager()
	memory := &otherTestStore{MemoryStore: store.NewMemoryStore()}
	manager := NewGroupManager(database, memory, settings, NewSubGroupManager(memory), nil)
	if err := manager.Initialize(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { manager.Stop(context.Background()) })
	return &GroupService{
		db: database, settingsManager: settings, groupManager: manager,
		aggregateGroupService: NewAggregateGroupService(database, manager),
		channelRegistry:       []string{"openai", "other"},
	}
}

func otherCreateParams(name string) GroupCreateParams {
	return GroupCreateParams{
		Name: name, ChannelType: "other",
		Upstreams: json.RawMessage(`[{"url":"https://example.com","weight":1}]`),
	}
}

func assertPersistedRetries(t *testing.T, service *GroupService, group *models.Group, want int) {
	t.Helper()
	var stored models.Group
	if err := service.db.First(&stored, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []*models.Group{group, &stored} {
		value, present := candidate.Config["max_retries"]
		if !present || value == nil {
			t.Fatalf("missing persisted override: %#v", candidate.Config)
		}
		if got := service.settingsManager.GetEffectiveConfig(candidate.Config).MaxRetries; got != want {
			t.Fatalf("effective max_retries = %d, want %d; config=%#v", got, want, candidate.Config)
		}
	}
}

func assertOtherBadRequest(t *testing.T, err error) {
	t.Helper()
	i18nErr, ok := err.(*I18nError)
	if !ok || i18nErr.APIError.HTTPStatus != http.StatusBadRequest || i18nErr.APIError.Code != app_errors.ErrBadRequest.Code ||
		(i18nErr.MessageID != "validation.other_standard_only" && i18nErr.MessageID != "validation.other_cannot_be_sub_group") {
		t.Fatalf("expected BAD_REQUEST, got %T: %v", err, err)
	}
}

func TestOtherCreateRetryOverrides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   int
	}{
		{"missing", nil, 0},
		{"empty", map[string]any{}, 0},
		{"null", map[string]any{"max_retries": nil}, 0},
		{"zero", map[string]any{"max_retries": float64(0)}, 0},
		{"positive", map[string]any{"max_retries": float64(5)}, 5},
		{"typed_positive", map[string]any{"max_retries": 7}, 7},
		{"other_override", map[string]any{"request_timeout": float64(60)}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newOtherTestService(t)
			params := otherCreateParams(tc.name)
			params.Config = tc.config
			group, err := service.CreateGroup(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if group.GroupType != "standard" || group.TestModel != "" || group.ValidationEndpoint != "" {
				t.Fatalf("other defaults = %#v", group)
			}
			assertPersistedRetries(t, service, group, tc.want)
			if tc.name == "other_override" && group.Config["request_timeout"] != float64(60) {
				t.Fatalf("lost unrelated override: %#v", group.Config)
			}
		})
	}
}

func TestOtherPreservesAIFieldsAndAllowsEmptyTestModel(t *testing.T) {
	service := newOtherTestService(t)
	params := otherCreateParams("ai_fields")
	params.TestModel = " stored-model "
	params.ValidationEndpoint = "/stored/endpoint"
	params.ParamOverrides = map[string]any{"model": "stored-override"}
	params.ModelRedirectRules = map[string]string{"source": "destination"}
	params.ModelRedirectStrict = true
	group, err := service.CreateGroup(context.Background(), params)
	if err != nil {
		t.Fatal(err)
	}
	description := "updated"
	group, err = service.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{Description: &description})
	if err != nil {
		t.Fatal(err)
	}
	var stored models.Group
	if err := service.db.First(&stored, group.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.TestModel != "stored-model" || stored.ValidationEndpoint != params.ValidationEndpoint ||
		stored.ParamOverrides["model"] != "stored-override" || stored.ModelRedirectRules["source"] != "destination" || !stored.ModelRedirectStrict {
		t.Fatalf("AI fields not preserved: %#v", stored)
	}
	group, err = service.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{HasTestModel: true, TestModel: ""})
	if err != nil {
		t.Fatal(err)
	}
	if group.TestModel != "" {
		t.Fatalf("explicit empty model ignored: %q", group.TestModel)
	}
	assertPersistedRetries(t, service, group, 0)
}

func TestOtherRetryDefaultsOnEveryUpdate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stored   datatypes.JSONMap
		incoming map[string]any
		want     int
	}{
		{"legacy_missing", nil, nil, 0},
		{"legacy_null", datatypes.JSONMap{"max_retries": nil}, nil, 0},
		{"preserve_unrelated", datatypes.JSONMap{"request_timeout": float64(60)}, nil, 0},
		{"legacy_invalid", datatypes.JSONMap{"max_retries": "bad"}, nil, 0},
		{"legacy_negative", datatypes.JSONMap{"max_retries": -1}, nil, 0},
		{"keep_positive", datatypes.JSONMap{"max_retries": 4}, nil, 4},
		{"remove_override", datatypes.JSONMap{"max_retries": 4}, map[string]any{}, 0},
		{"null_override", datatypes.JSONMap{"max_retries": 4}, map[string]any{"max_retries": nil}, 0},
		{"replace_config", datatypes.JSONMap{"max_retries": 4}, map[string]any{"request_timeout": float64(60)}, 0},
		{"explicit_positive", nil, map[string]any{"max_retries": float64(6)}, 6},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newOtherTestService(t)
			group := models.Group{Name: tc.name, GroupType: "standard", ChannelType: "other", Upstreams: datatypes.JSON("[]"), Config: tc.stored}
			if err := service.db.Create(&group).Error; err != nil {
				t.Fatal(err)
			}
			description := "unrelated update"
			updated, err := service.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{Description: &description, Config: tc.incoming})
			if err != nil {
				t.Fatal(err)
			}
			assertPersistedRetries(t, service, updated, tc.want)
			if tc.name == "preserve_unrelated" && service.settingsManager.GetEffectiveConfig(updated.Config).RequestTimeout != 60 {
				t.Fatalf("lost existing unrelated override: %#v", updated.Config)
			}
		})
	}
}

func TestOtherChannelTransitions(t *testing.T) {
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   int
	}{
		{"inherited", nil, 0},
		{"zero", map[string]any{"max_retries": float64(0)}, 0},
		{"positive", map[string]any{"max_retries": float64(4)}, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newOtherTestService(t)
			params := otherCreateParams(tc.name)
			params.ChannelType = "openai"
			params.TestModel = "retained-model"
			params.ValidationEndpoint = "/retained/endpoint"
			params.Config = tc.config
			group, err := service.CreateGroup(context.Background(), params)
			if err != nil {
				t.Fatal(err)
			}
			if tc.config == nil {
				if _, ok := group.Config["max_retries"]; ok {
					t.Fatal("AI channel gained retry override")
				}
				if got := service.settingsManager.GetEffectiveConfig(group.Config).MaxRetries; got != utils.DefaultSystemSettings().MaxRetries {
					t.Fatalf("AI default changed: %d", got)
				}
			}
			other := "other"
			group, err = service.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{ChannelType: &other})
			if err != nil {
				t.Fatal(err)
			}
			assertPersistedRetries(t, service, group, tc.want)
			if group.TestModel != params.TestModel || group.ValidationEndpoint != params.ValidationEndpoint {
				t.Fatal("channel switch cleared AI fields")
			}
			openai := "openai"
			group, err = service.UpdateGroup(context.Background(), group.ID, GroupUpdateParams{ChannelType: &openai})
			if err != nil {
				t.Fatal(err)
			}
			assertPersistedRetries(t, service, group, tc.want)
		})
	}
}

func TestOtherGroupConstraints(t *testing.T) {
	service := newOtherTestService(t)
	ctx := context.Background()
	params := otherCreateParams("aggregate_other")
	params.GroupType = "aggregate"
	_, err := service.CreateGroup(ctx, params)
	assertOtherBadRequest(t, err)

	params = otherCreateParams("aggregate_ai")
	params.GroupType = "aggregate"
	params.ChannelType = "openai"
	aggregate, err := service.CreateGroup(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	other := "other"
	_, err = service.UpdateGroup(ctx, aggregate.ID, GroupUpdateParams{ChannelType: &other})
	assertOtherBadRequest(t, err)

	standard, err := service.CreateGroup(ctx, otherCreateParams("standard_other"))
	if err != nil {
		t.Fatal(err)
	}
	aggregateType := "aggregate"
	_, err = service.UpdateGroup(ctx, standard.ID, GroupUpdateParams{GroupType: &aggregateType})
	assertOtherBadRequest(t, err)
	input := []SubGroupInput{{GroupID: standard.ID, Weight: 1}}
	_, err = service.aggregateGroupService.ValidateSubGroups(ctx, "openai", input, "")
	assertOtherBadRequest(t, err)
	_, err = service.aggregateGroupService.ValidateSubGroups(ctx, "other", input, "")
	assertOtherBadRequest(t, err)
	err = service.aggregateGroupService.AddSubGroups(ctx, aggregate.ID, input)
	assertOtherBadRequest(t, err)
	var count int64
	if err := service.db.Model(&models.GroupSubGroup{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rejected sub-group was persisted: %d", count)
	}

	params = otherCreateParams("referenced_ai")
	params.ChannelType, params.TestModel = "openai", "model"
	ai, err := service.CreateGroup(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.aggregateGroupService.AddSubGroups(ctx, aggregate.ID, []SubGroupInput{{GroupID: ai.ID, Weight: 1}}); err != nil {
		t.Fatal(err)
	}
	_, err = service.UpdateGroup(ctx, ai.ID, GroupUpdateParams{ChannelType: &other})
	if err == nil {
		t.Fatal("referenced sub-group switched to other")
	}
}

func TestOtherAndAIValidation(t *testing.T) {
	service := newOtherTestService(t)
	ctx := context.Background()
	params := otherCreateParams("ai_missing_model")
	params.ChannelType = "openai"
	if _, err := service.CreateGroup(ctx, params); err == nil {
		t.Fatal("AI model should remain required")
	}

	params = otherCreateParams("invalid_endpoint")
	params.ValidationEndpoint = "https://invalid/path"
	if _, err := service.CreateGroup(ctx, params); err == nil {
		t.Fatal("invalid endpoint accepted")
	}
	params = otherCreateParams("invalid_upstreams")
	params.Upstreams = json.RawMessage("[]")
	if _, err := service.CreateGroup(ctx, params); err == nil {
		t.Fatal("empty upstreams accepted")
	}

	for i, invalid := range []any{float64(-1), -1, float64(1.5), "3", true} {
		params = otherCreateParams("invalid_retries")
		params.Config = map[string]any{"max_retries": invalid}
		if _, err := service.CreateGroup(ctx, params); err == nil {
			t.Fatalf("invalid retry case %d accepted: %#v", i, invalid)
		}
	}
	group, err := service.CreateGroup(ctx, otherCreateParams("empty_model"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateGroup(ctx, group.ID, GroupUpdateParams{Config: map[string]any{"max_retries": float64(-1)}}); err == nil {
		t.Fatal("invalid retry update accepted")
	}
	assertPersistedRetries(t, service, group, 0)
	openai := "openai"
	if _, err := service.UpdateGroup(ctx, group.ID, GroupUpdateParams{ChannelType: &openai}); err == nil {
		t.Fatal("switch to AI without test model accepted")
	}
	group, err = service.UpdateGroup(ctx, group.ID, GroupUpdateParams{ChannelType: &openai, TestModel: "model", HasTestModel: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.UpdateGroup(ctx, group.ID, GroupUpdateParams{HasTestModel: true, TestModel: ""}); err == nil {
		t.Fatal("AI empty model accepted")
	}
	assertPersistedRetries(t, service, group, 0)
}

func TestOtherCopyPersistsRetryDefault(t *testing.T) {
	service := newOtherTestService(t)
	source := models.Group{Name: "legacy", ChannelType: "other", GroupType: "standard", Upstreams: datatypes.JSON("[]"), TestModel: "stored-model"}
	if err := service.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	copy, err := service.CopyGroup(context.Background(), source.ID, "none")
	if err != nil {
		t.Fatal(err)
	}
	assertPersistedRetries(t, service, copy, 0)
	if copy.TestModel != source.TestModel {
		t.Fatal("copy cleared stored model")
	}
}

func TestOtherConstraintTranslations(t *testing.T) {
	for language, messages := range map[string]map[string]string{
		"zh-CN": locales.MessagesZhCN,
		"en-US": locales.MessagesEnUS,
		"ja-JP": locales.MessagesJaJP,
	} {
		for _, key := range []string{"validation.other_standard_only", "validation.other_cannot_be_sub_group"} {
			if messages[key] == "" {
				t.Fatalf("missing %s translation: %s", language, key)
			}
		}
	}
}

func TestEnsureOtherMaxRetriesDoesNotMutateInput(t *testing.T) {
	input := map[string]any{"max_retries": nil, "request_timeout": float64(60)}
	output := ensureOtherMaxRetries(input)
	if input["max_retries"] != nil || output["max_retries"] != float64(0) || output["request_timeout"] != float64(60) {
		t.Fatalf("input=%#v output=%#v", input, output)
	}
}
