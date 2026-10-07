package keypool

import (
	"reflect"
	"testing"
	"time"

	"gpt-load/internal/config"
	"gpt-load/internal/models"

	"gorm.io/gorm"
)

func TestCronCheckerOtherDirectEntryNeedsNoFacilities(t *testing.T) {
	lastValidated := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	group := &models.Group{ChannelType: "other", LastValidatedAt: &lastValidated}
	before := *group
	(&CronChecker{}).validateGroupKeys(group)
	if !reflect.DeepEqual(*group, before) {
		t.Fatal("other direct validation mutated the group")
	}
}

func TestCronCheckerFiltersOtherBeforeConfigResolution(t *testing.T) {
	db := newKeypoolTestDB(t)
	lastValidated := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	group := createKeypoolTestGroup(t, db, "other", "other", "standard", &lastValidated)
	nullTypeGroup := createKeypoolTestGroup(t, db, "legacy-other", "other", "standard", nil)
	if err := db.Model(nullTypeGroup).Update("group_type", nil).Error; err != nil {
		t.Fatal(err)
	}
	createKeypoolTestGroup(t, db, "aggregate", "openai", "aggregate", nil)
	key := models.APIKey{GroupID: group.ID, KeyValue: "test", Status: models.KeyStatusInvalid, FailureCount: 8}
	if err := db.Create(&key).Error; err != nil {
		t.Fatal(err)
	}
	var keyBefore models.APIKey
	if err := db.First(&keyBefore, key.ID).Error; err != nil {
		t.Fatal(err)
	}

	queriedGroups := false
	if err := db.Callback().Query().After("gorm:query").Register("test:groups_filter", func(tx *gorm.DB) {
		if groups, ok := tx.Statement.Dest.(*[]models.Group); ok {
			queriedGroups = true
			if len(*groups) != 0 {
				t.Errorf("cron query returned excluded groups: %+v", *groups)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	// 配置、校验和解密设施全部为空，证明过滤发生在访问这些设施之前。
	checker := &CronChecker{DB: db}
	checker.submitValidationJobs()
	if !queriedGroups {
		t.Fatal("cron did not query groups")
	}
	if err := db.Callback().Query().Remove("test:groups_filter"); err != nil {
		t.Fatal(err)
	}

	for _, original := range []*models.Group{group, nullTypeGroup} {
		var stored models.Group
		if err := db.First(&stored, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored.LastValidatedAt, original.LastValidatedAt) {
			t.Fatalf("last_validated_at changed for %s", original.Name)
		}
		checker.validateGroupKeys(&stored)
		var after models.Group
		if err := db.First(&after, original.ID).Error; err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, after) {
			t.Fatalf("direct cron entry changed group %s", original.Name)
		}
	}
	var storedKey models.APIKey
	if err := db.First(&storedKey, key.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedKey, keyBefore) {
		t.Fatal("cron changed other key state")
	}
}

func TestCronCheckerKeepsExistingChannelsAndLegacyGroups(t *testing.T) {
	db := newKeypoolTestDB(t)
	standard := createKeypoolTestGroup(t, db, "openai", "openai", "standard", nil)
	legacy := createKeypoolTestGroup(t, db, "gemini", "gemini", "standard", nil)
	if err := db.Model(legacy).Update("group_type", nil).Error; err != nil {
		t.Fatal(err)
	}
	checker := &CronChecker{DB: db, SettingsManager: config.NewSystemSettingsManager()}
	checker.submitValidationJobs()
	for _, group := range []*models.Group{standard, legacy} {
		var stored models.Group
		if err := db.First(&stored, group.ID).Error; err != nil {
			t.Fatal(err)
		}
		if stored.LastValidatedAt == nil {
			t.Fatalf("existing channel %s was unexpectedly skipped", group.Name)
		}
	}
}
