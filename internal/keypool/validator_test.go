package keypool

import (
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/encryption"
	"gpt-load/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

func newKeypoolTestDB(t *testing.T) *gorm.DB {
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
	if err := db.AutoMigrate(&models.Group{}, &models.APIKey{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func createKeypoolTestGroup(t *testing.T, db *gorm.DB, name, channelType, groupType string, lastValidatedAt *time.Time) *models.Group {
	t.Helper()
	group := &models.Group{
		Name: name, ChannelType: channelType, GroupType: groupType,
		Upstreams: datatypes.JSON("[]"), TestModel: "test", LastValidatedAt: lastValidatedAt,
	}
	if err := db.Create(group).Error; err != nil {
		t.Fatal(err)
	}
	return group
}

func TestValidateSingleKeyOtherNeedsNoFacilities(t *testing.T) {
	validator := &KeyValidator{}
	key := &models.APIKey{ID: 7, KeyValue: "test", Status: models.KeyStatusInvalid, FailureCount: 12}
	group := &models.Group{
		ChannelType: "other", GroupType: "standard",
		Config: datatypes.JSONMap{"key_validation_timeout_seconds": "invalid"},
	}
	keyBefore, groupBefore := *key, *group
	for _, input := range []*models.APIKey{key, nil} {
		valid, err := validator.ValidateSingleKey(input, group)
		if !valid || err != nil {
			t.Fatalf("valid = %v, err = %v; want true, nil", valid, err)
		}
	}
	if !reflect.DeepEqual(*key, keyBefore) || !reflect.DeepEqual(*group, groupBefore) {
		t.Fatal("other validation mutated the key or group config")
	}
}

func TestTestMultipleKeysOtherStillChecksDatabase(t *testing.T) {
	db := newKeypoolTestDB(t)
	group := createKeypoolTestGroup(t, db, "other", "other", "standard", nil)
	otherGroup := createKeypoolTestGroup(t, db, "another", "other", "standard", nil)
	enc, err := encryption.NewService("")
	if err != nil {
		t.Fatal(err)
	}
	keys := []models.APIKey{
		{KeyValue: "existing-invalid", KeyHash: enc.Hash("existing-invalid"), GroupID: group.ID, Status: models.KeyStatusInvalid, FailureCount: 9, RequestCount: 3},
		{KeyValue: "existing-active", KeyHash: enc.Hash("existing-active"), GroupID: group.ID, Status: models.KeyStatusActive, FailureCount: 2},
		{KeyValue: "wrong-group", KeyHash: enc.Hash("wrong-group"), GroupID: otherGroup.ID, Status: models.KeyStatusInvalid, FailureCount: 5},
	}
	if err := db.Create(&keys).Error; err != nil {
		t.Fatal(err)
	}
	var before []models.APIKey
	if err := db.Order("id").Find(&before).Error; err != nil {
		t.Fatal(err)
	}

	// 仅提供批量存在性检查所需设施；校验不得访问配置、工厂或状态更新设施。
	validator := &KeyValidator{DB: db, encryptionSvc: enc}
	values := []string{"existing-invalid", "missing", "wrong-group", "existing-active"}
	results, err := validator.TestMultipleKeys(group, values)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(values) {
		t.Fatalf("got %d results, want %d", len(results), len(values))
	}
	for i, result := range results {
		wantValid := i == 0 || i == 3
		if result.KeyValue != values[i] || result.IsValid != wantValid {
			t.Fatalf("unexpected result at %d: %+v", i, result)
		}
		if wantValid && result.Error != "" || !wantValid && result.Error != "Key does not exist in this group or has been removed." {
			t.Fatalf("unexpected error at %d: %q", i, result.Error)
		}
	}
	var after []models.APIKey
	if err := db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("other batch validation changed persisted key state")
	}
	if group.EffectiveConfig.AppUrl != "" {
		t.Fatal("other batch validation resolved effective config")
	}
}
