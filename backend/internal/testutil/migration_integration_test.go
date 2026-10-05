package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"gofeed/internal/auth"
	"gofeed/internal/social"
	"gofeed/internal/user"
	"gofeed/internal/video"
)

// expectedVideoColumns 是公开读取不变量依赖的 videos 列定义
var expectedVideoColumns = []expectedColumn{
	{name: "id", columnType: "bigint unsigned", nullable: "NO"},
	{name: "author_id", columnType: "bigint unsigned", nullable: "NO"},
	{name: "title", columnType: "varchar(255)", nullable: "NO"},
	{name: "description", columnType: "varchar(1000)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "play_url", columnType: "varchar(512)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "play_file_name", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "play_original_name", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "cover_url", columnType: "varchar(512)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "cover_file_name", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "cover_original_name", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "status", columnType: "varchar(16)", nullable: "NO", defaultValue: stringPtr("published")},
	{name: "rejected_reason", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "rejected_at", columnType: "datetime(3)", nullable: "YES"},
	{name: "published_at", columnType: "datetime(3)", nullable: "YES"},
	// 围栏令牌必须按字节比较，大小写不同的令牌不能互相通过围栏校验
	{name: "purge_token", columnType: "char(32)", nullable: "YES", charset: "ascii"},
	{name: "purge_lease_until", columnType: "datetime(3)", nullable: "YES"},
	{name: "play_purged_at", columnType: "datetime(3)", nullable: "YES"},
	{name: "cover_purged_at", columnType: "datetime(3)", nullable: "YES"},
	{name: "created_at", columnType: "datetime(3)", nullable: "NO"},
	{name: "updated_at", columnType: "datetime(3)", nullable: "NO"},
	{name: "deleted_at", columnType: "datetime(3)", nullable: "YES"},
}

// expectedVideoIndexes 是公开排序与清扫候选查询依赖的 videos 索引定义
var expectedVideoIndexes = []expectedIndex{
	{
		name:       "idx_videos_published_id",
		columns:    []string{"published_at", "id"},
		descending: map[int]bool{1: true, 2: true},
	},
	{
		name:       "idx_videos_author_published",
		columns:    []string{"author_id", "published_at"},
		descending: map[int]bool{2: true},
	},
	{name: "idx_videos_draft_created", columns: []string{"status", "created_at", "id"}},
	{name: "idx_videos_purging_lease", columns: []string{"status", "purge_lease_until", "id"}},
	{name: "idx_videos_rejected_purge", columns: []string{"status", "rejected_at", "id"}},
}

// expectedOutboxColumns 是 outbox 仓储契约依赖的列定义，租约四列由既有用例覆盖
var expectedOutboxColumns = []expectedColumn{
	{name: "id", columnType: "bigint unsigned", nullable: "NO"},
	{name: "event_id", columnType: "char(36)", nullable: "NO"},
	{name: "video_id", columnType: "bigint unsigned", nullable: "NO"},
	{name: "event_type", columnType: "varchar(64)", nullable: "NO"},
	{name: "status", columnType: "varchar(16)", nullable: "NO", defaultValue: stringPtr("pending")},
	{name: "attempt", columnType: "int", nullable: "NO", defaultValue: stringPtr("0")},
	{name: "last_error", columnType: "varchar(255)", nullable: "NO", defaultValue: stringPtr("")},
	{name: "created_at", columnType: "datetime(3)", nullable: "NO"},
	{name: "dispatched_at", columnType: "datetime(3)", nullable: "YES"},
}

// expectedOutboxIndexes 是 outbox 去重与快照查询依赖的索引定义，claim 索引由既有用例覆盖
var expectedOutboxIndexes = []expectedIndex{
	{name: "uq_video_outbox_events_event_id", columns: []string{"event_id"}, unique: true},
	{name: "idx_video_outbox_events_video", columns: []string{"video_id"}},
}

// actualColumn 保存 information_schema 返回的单个列定义
type actualColumn struct {
	ColumnName    string  `gorm:"column:COLUMN_NAME"`
	ColumnType    string  `gorm:"column:COLUMN_TYPE"`
	IsNullable    string  `gorm:"column:IS_NULLABLE"`
	ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
	Charset       *string `gorm:"column:CHARACTER_SET_NAME"`
}

// actualIndexColumn 保存 information_schema 返回的单个索引列
type actualIndexColumn struct {
	IndexName  string  `gorm:"column:INDEX_NAME"`
	SeqInIndex int     `gorm:"column:SEQ_IN_INDEX"`
	ColumnName string  `gorm:"column:COLUMN_NAME"`
	NonUnique  int     `gorm:"column:NON_UNIQUE"`
	Collation  *string `gorm:"column:COLLATION"`
}

// actualIndex 保存按索引名归并后的列顺序、唯一性与降序位置
type actualIndex struct {
	name       string
	columns    []string
	unique     bool
	descending map[int]bool
}

// modelSchemaTargets 是参与表级对齐检查的业务模型
var modelSchemaTargets = []any{
	&user.User{},
	&auth.AuthSession{},
	&video.Video{},
	&video.OutboxEvent{},
	&social.VideoLike{},
	&social.Follow{},
	&social.Comment{},
}

// parseModelSchema 解析模型得到表名与字段定义
func parseModelSchema(t *testing.T, model any) *schema.Schema {
	t.Helper()
	parsed, err := schema.Parse(model, &sync.Map{}, schema.NamingStrategy{})
	if err != nil {
		t.Fatalf("解析模型 %T 失败: %v", model, err)
	}
	return parsed
}

// modelColumnNames 返回模型声明的数据库列名
func modelColumnNames(parsed *schema.Schema) []string {
	names := make([]string, 0, len(parsed.Fields))
	for _, field := range parsed.Fields {
		names = append(names, field.DBName)
	}
	return names
}

// tableColumnNames 返回实际表的列名
func tableColumnNames(columns map[string]actualColumn) []string {
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	return names
}

// 测试目标：验证 videos 公开读取不变量依赖的列定义与迁移声明一致
// 预期效果：媒体列非空且默认空串，发布时间与软删除列可空，状态列默认 published
func TestMigrationProductVideosColumnsMatchPublicReadInvariant(t *testing.T) {
	db := DB(t)
	actual := readTableColumns(t, db, "videos")
	if len(actual) == 0 {
		t.Fatal("迁移产物缺少 videos 表")
	}

	for _, want := range expectedVideoColumns {
		got, ok := actual[want.name]
		if !ok {
			t.Errorf("videos 缺少列 %s", want.name)
			continue
		}
		for _, difference := range compareColumn(want, got) {
			t.Errorf("videos 列 %s %s", want.name, difference)
		}
	}
}

// 测试目标：验证公开排序与清扫候选索引的列顺序与排序方向
// 预期效果：公开列表索引按发布时间与标识降序，清扫索引按状态与时间标识升序
func TestMigrationProductIndexesMatchDeclaredOrdering(t *testing.T) {
	db := DB(t)
	actual := readTableIndexes(t, db, "videos")
	if len(actual) == 0 {
		t.Fatal("迁移产物缺少 videos 索引")
	}
	descendingSupported := supportsDescendingIndex(t, db)

	for _, want := range expectedVideoIndexes {
		got, ok := actual[want.name]
		if !ok {
			t.Errorf("videos 缺少索引 %s", want.name)
			continue
		}
		if !sameStringSequence(got.columns, want.columns) {
			t.Errorf("videos 索引 %s 的列为 %v 期望 %v", want.name, got.columns, want.columns)
		}
		if got.unique != want.unique {
			t.Errorf("videos 索引 %s 的唯一性为 %v 期望 %v", want.name, got.unique, want.unique)
		}
		if !descendingSupported {
			continue
		}
		for position := range want.descending {
			if !got.descending[position] {
				t.Errorf("videos 索引 %s 第 %d 列应为降序排列", want.name, position)
			}
		}
	}
}

// 测试目标：验证 outbox 表在真实迁移产物上的列、唯一键与视频外键定义
// 预期效果：事件标识唯一，状态与重试计数带默认值，视频外键按级联删除
func TestMigrationProductOutboxDefinitionMatchesRepoContract(t *testing.T) {
	db := DB(t)
	actual := readTableColumns(t, db, "video_outbox_events")
	if len(actual) == 0 {
		t.Fatal("迁移产物缺少 video_outbox_events 表")
	}
	for _, want := range expectedOutboxColumns {
		got, ok := actual[want.name]
		if !ok {
			t.Errorf("video_outbox_events 缺少列 %s", want.name)
			continue
		}
		for _, difference := range compareColumn(want, got) {
			t.Errorf("video_outbox_events 列 %s %s", want.name, difference)
		}
	}

	indexes := readTableIndexes(t, db, "video_outbox_events")
	for _, want := range expectedOutboxIndexes {
		got, ok := indexes[want.name]
		if !ok {
			t.Errorf("video_outbox_events 缺少索引 %s", want.name)
			continue
		}
		if !sameStringSequence(got.columns, want.columns) {
			t.Errorf("video_outbox_events 索引 %s 的列为 %v 期望 %v", want.name, got.columns, want.columns)
		}
		if got.unique != want.unique {
			t.Errorf("video_outbox_events 索引 %s 的唯一性为 %v 期望 %v", want.name, got.unique, want.unique)
		}
	}
}

// 测试目标：验证模型声明的索引名在真实迁移产物上都存在
// 预期效果：模型标签与迁移索引一一对应，不会出现模型侧新增索引而未落库
func TestModelDeclaredIndexesExistInMigrationProduct(t *testing.T) {
	db := DB(t)
	checks := []struct {
		model   any
		indexes []string
	}{
		{
			model: &video.Video{},
			indexes: []string{
				"idx_videos_published_id", "idx_videos_author_published", "idx_videos_rejected_purge",
				"idx_videos_status", "idx_videos_deleted_at",
			},
		},
		{
			model:   &video.OutboxEvent{},
			indexes: []string{"uq_video_outbox_events_event_id", "idx_video_outbox_events_video"},
		},
		{model: &user.User{}, indexes: []string{"idx_users_deleted_at"}},
		{
			model:   &auth.AuthSession{},
			indexes: []string{"idx_auth_sessions_user_active", "idx_auth_sessions_expires_at"},
		},
		{model: &social.VideoLike{}, indexes: []string{"uq_video_likes_video_user"}},
		{model: &social.Follow{}, indexes: []string{"uq_user_follows_follower_followee"}},
		{
			model:   &social.Comment{},
			indexes: []string{"idx_video_comments_video_visible", "idx_video_comments_author_visible"},
		},
	}
	for _, check := range checks {
		for _, name := range check.indexes {
			if !db.Migrator().HasIndex(check.model, name) {
				t.Errorf("模型 %T 声明的索引 %s 在真实迁移产物中不存在", check.model, name)
			}
		}
	}
}

// 测试目标：验证模型字段集合与真实迁移产物的列集合完全相同
// 预期效果：七张表都不缺列也不多列，模型与迁移不会各自漂移
func TestModelColumnSetsMatchMigrationProduct(t *testing.T) {
	db := DB(t)
	for _, target := range modelSchemaTargets {
		parsed := parseModelSchema(t, target)
		actual := readTableColumns(t, db, parsed.Table)
		if len(actual) == 0 {
			t.Errorf("迁移产物缺少模型 %T 对应的表 %s", target, parsed.Table)
			continue
		}
		declared := modelColumnNames(parsed)
		physical := tableColumnNames(actual)
		if sameStringSet(declared, physical) {
			continue
		}
		t.Errorf(
			"表 %s 的模型列与迁移列不一致 模型缺少=%v 迁移多出=%v",
			parsed.Table, subtractStringSet(declared, physical), subtractStringSet(physical, declared),
		)
	}
}

// 测试目标：验证真实迁移产物上的唯一约束与外键约束确实被服务端强制执行
// 预期效果：重复事件标识被拒，悬空视频引用被拒，父行物理删除时事件级联移除
func TestMigrationProductConstraintsAreEnforced(t *testing.T) {
	db := DB(t)

	if err := db.Exec(
		"INSERT INTO videos (author_id, title, created_at, updated_at) VALUES (?, ?, NOW(3), NOW(3))",
		1, "constraint-parent",
	).Error; err != nil {
		t.Fatalf("写入父视频行失败: %v", err)
	}
	var parentID uint
	if err := db.Raw("SELECT id FROM videos WHERE title = ?", "constraint-parent").Scan(&parentID).Error; err != nil {
		t.Fatalf("读取父视频行标识失败: %v", err)
	}

	const eventID = "0f9a1d2c-3b4e-4f5a-8c7d-9e0f1a2b3c4d"
	insertEvent := func(identifier string, videoID uint) error {
		return db.Exec(
			"INSERT INTO video_outbox_events (event_id, video_id, event_type, created_at) VALUES (?, ?, ?, NOW(3))",
			identifier, videoID, "video.process",
		).Error
	}
	if err := insertEvent(eventID, parentID); err != nil {
		t.Fatalf("写入 outbox 事件失败: %v", err)
	}
	if err := insertEvent(eventID, parentID); !isDuplicateEntryError(err) {
		t.Errorf("重复 event_id 未被唯一约束拒绝 err=%v", err)
	}
	if err := insertEvent("1a2b3c4d-5e6f-4a7b-8c9d-0e1f2a3b4c5d", 4294967295); !isForeignKeyViolationError(err) {
		t.Errorf("悬空 video_id 未被外键拒绝 err=%v", err)
	}

	if err := db.Exec("DELETE FROM videos WHERE id = ?", parentID).Error; err != nil {
		t.Fatalf("物理删除父视频行失败: %v", err)
	}
	var remaining int64
	if err := db.Raw(
		"SELECT COUNT(*) FROM video_outbox_events WHERE video_id = ?", parentID,
	).Scan(&remaining).Error; err != nil {
		t.Fatalf("统计级联后的 outbox 事件失败: %v", err)
	}
	if remaining != 0 {
		t.Errorf("父行删除后仍有 %d 条 outbox 事件，级联删除未生效", remaining)
	}
}

// 测试目标：验证绕过模型默认值写入的原始行仍被公开读取谓词正确判定
// 预期效果：省略媒体列的行拿到空串媒体与空发布时间，完整发布行是唯一可见行
func TestMigrationProductRawRowsMatchPublicVideoQuery(t *testing.T) {
	db := DB(t)
	repo := video.NewRepository(db)
	ctx := context.Background()

	// 测试目标：不提供状态与媒体列，检验迁移声明的列默认值
	// 预期效果：状态取默认 published，媒体列取空串，发布时间保持为空
	if err := db.Exec(
		"INSERT INTO videos (author_id, title, created_at, updated_at) VALUES (?, ?, NOW(3), NOW(3))",
		1, "defaults-row",
	).Error; err != nil {
		t.Fatalf("写入默认值行失败: %v", err)
	}
	var defaults video.Video
	if err := db.Raw("SELECT * FROM videos WHERE title = ?", "defaults-row").Scan(&defaults).Error; err != nil {
		t.Fatalf("读取默认值行失败: %v", err)
	}
	if defaults.Status != video.VideoStatusPublished {
		t.Errorf("状态列默认值错误 got=%q want=%q", defaults.Status, video.VideoStatusPublished)
	}
	if defaults.PublishedAt != nil {
		t.Errorf("published_at 应保持为空 got=%v", defaults.PublishedAt)
	}
	for name, value := range map[string]string{
		"play_url":            defaults.PlayURL,
		"play_file_name":      defaults.PlayFileName,
		"play_original_name":  defaults.PlayOriginalName,
		"cover_url":           defaults.CoverURL,
		"cover_file_name":     defaults.CoverFileName,
		"cover_original_name": defaults.CoverOriginalName,
	} {
		if value != "" {
			t.Errorf("媒体列 %s 的默认值应为空串 got=%q", name, value)
		}
	}

	insertRaw := func(status string, deleted bool) uint {
		t.Helper()
		deletedAt := "NULL"
		if deleted {
			deletedAt = "NOW(3)"
		}
		statement := `INSERT INTO videos (
			author_id, title, description, play_url, play_file_name, play_original_name,
			cover_url, cover_file_name, cover_original_name, status, published_at, created_at, updated_at, deleted_at
		) VALUES (?, ?, '', '/static/videos/1/raw.mp4', 'raw.mp4', '原始名.mp4',
			'/static/covers/1/raw.webp', 'raw.webp', '封面名.webp', ?, NOW(3), NOW(3), NOW(3), ` + deletedAt + `)`
		if err := db.Exec(statement, 1, "raw-"+status, status).Error; err != nil {
			t.Fatalf("写入原始 %s 行失败: %v", status, err)
		}
		var id uint
		if err := db.Raw("SELECT id FROM videos WHERE title = ?", "raw-"+status).Scan(&id).Error; err != nil {
			t.Fatalf("读取原始 %s 行标识失败: %v", status, err)
		}
		return id
	}
	visibleID := insertRaw(video.VideoStatusPublished, false)
	draftID := insertRaw(video.VideoStatusDraft, false)
	deletedID := insertRaw(video.VideoStatusPublished, true)

	var visible []video.Video
	if err := video.PublicVideoQuery(db).Order("id ASC").Find(&visible).Error; err != nil {
		t.Fatalf("按公开谓词查询失败: %v", err)
	}
	if len(visible) != 1 || visible[0].ID != visibleID {
		t.Fatalf("公开谓词应只命中完整发布行 got=%+v want=%d", visible, visibleID)
	}

	// 测试目标：比对数据库层公开谓词与 Go 侧公开不变量
	// 预期效果：同一批原始行在两侧得到完全一致的可见性判定
	ids := []uint{defaults.ID, visibleID, draftID, deletedID}
	var all []video.Video
	if err := db.Unscoped().Where("id IN ?", ids).Order("id ASC").Find(&all).Error; err != nil {
		t.Fatalf("读取全部原始行失败: %v", err)
	}
	if len(all) != len(ids) {
		t.Fatalf("原始行数量错误 got=%d want=%d", len(all), len(ids))
	}
	for _, row := range all {
		want := row.ID == visibleID
		if got := video.IsPublicVideo(row); got != want {
			t.Errorf("视频 %d 的公开不变量为 %v 期望 %v", row.ID, got, want)
		}
	}

	listed, err := repo.GetPublishedVideoList(ctx, 0, nil, 100)
	if err != nil {
		t.Fatalf("查询公开列表失败: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != visibleID {
		t.Fatalf("公开列表应只返回完整发布行 got=%+v want=%d", listed, visibleID)
	}
	batched, err := repo.GetPublishedByIDs(ctx, ids)
	if err != nil {
		t.Fatalf("批量读取公开视频失败: %v", err)
	}
	if len(batched) != 1 || batched[0].ID != visibleID {
		t.Fatalf("批量读取应只返回完整发布行 got=%+v want=%d", batched, visibleID)
	}
}

// readTableColumns 读取指定表的列定义
func readTableColumns(t *testing.T, db *gorm.DB, table string) map[string]actualColumn {
	t.Helper()
	var rows []actualColumn
	if err := db.Raw(`
		SELECT COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, CHARACTER_SET_NAME
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION
	`, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读取表 %s 的列元数据失败: %v", table, err)
	}
	columns := make(map[string]actualColumn, len(rows))
	for _, row := range rows {
		columns[row.ColumnName] = row
	}
	return columns
}

// readTableIndexes 读取指定表的索引定义并按索引名归并
func readTableIndexes(t *testing.T, db *gorm.DB, table string) map[string]actualIndex {
	t.Helper()
	var rows []actualIndexColumn
	if err := db.Raw(`
		SELECT INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE, COLLATION
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?
		ORDER BY INDEX_NAME, SEQ_IN_INDEX
	`, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读取表 %s 的索引元数据失败: %v", table, err)
	}
	indexes := make(map[string]actualIndex, len(rows))
	for _, row := range rows {
		index := indexes[row.IndexName]
		index.name = row.IndexName
		index.columns = append(index.columns, row.ColumnName)
		if row.NonUnique == 0 {
			index.unique = true
		}
		if row.Collation != nil && *row.Collation == "D" {
			if index.descending == nil {
				index.descending = make(map[int]bool, 1)
			}
			index.descending[row.SeqInIndex] = true
		}
		indexes[row.IndexName] = index
	}
	return indexes
}

// sameStringSequence 按顺序比较两个字符串切片
func sameStringSequence(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

// supportsDescendingIndex 判断服务端是否把降序声明落成真实的降序索引
func supportsDescendingIndex(t *testing.T, db *gorm.DB) bool {
	t.Helper()
	var version string
	if err := db.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		t.Fatalf("读取 MySQL 版本失败: %v", err)
	}
	t.Logf("真实 MySQL 版本 %s", version)
	return !strings.HasPrefix(version, "5.")
}

// isDuplicateEntryError 判断错误是否为唯一键冲突
func isDuplicateEntryError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlError *mysqldriver.MySQLError
	if errors.As(err, &mysqlError) {
		return mysqlError.Number == 1062
	}
	return strings.Contains(err.Error(), "Duplicate entry")
}

// isForeignKeyViolationError 判断错误是否为外键约束失败
func isForeignKeyViolationError(err error) bool {
	if err == nil {
		return false
	}
	var mysqlError *mysqldriver.MySQLError
	if errors.As(err, &mysqlError) {
		return mysqlError.Number == 1452
	}
	return strings.Contains(err.Error(), "foreign key constraint fails")
}

// 测试目标：配置模型结构集成测试进程
// 预期效果：运行前初始化并在结束后清理独立测试数据库
func TestMain(m *testing.M) {
	os.Exit(Main(m))
}

// 测试目标：验证 000004 迁移会接管旧版误软删的非公开视频
// 预期效果：旧草稿转为未软删的 purging 并成为草稿清扫候选
func TestDraftPurgeMigrationAdoptsLegacyDeletedDraft(t *testing.T) {
	db := DB(t)
	legacy := &video.Video{
		AuthorID: 1,
		Title:    "legacy deleted draft",
		Status:   video.VideoStatusDraft,
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("创建旧版草稿失败: %v", err)
	}
	if err := db.Exec("UPDATE videos SET deleted_at = ? WHERE id = ?", time.Now().Add(-time.Hour), legacy.ID).Error; err != nil {
		t.Fatalf("设置旧版软删除状态失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(migrationsDir(), "000004_draft_purge_lease.up.sql"))
	if err != nil {
		t.Fatalf("读取 000004 迁移失败: %v", err)
	}
	updateSQL := draftPurgeCompatibilityUpdate(string(content))
	if updateSQL == "" {
		t.Fatal("000004 迁移缺少旧版草稿兼容更新")
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		t.Fatalf("执行 000004 兼容更新失败: %v", err)
	}

	var got video.Video
	if err := db.Unscoped().First(&got, legacy.ID).Error; err != nil {
		t.Fatalf("读取兼容后的草稿失败: %v", err)
	}
	if got.Status != video.VideoStatusPurging || got.DeletedAt.Valid {
		t.Fatalf("兼容状态错误 status=%s deleted=%v", got.Status, got.DeletedAt.Valid)
	}

	ids, err := video.NewRepository(db).GetRecoverableDraftPurgeList(context.Background(), 10)
	if err != nil {
		t.Fatalf("查询兼容草稿清扫候选失败: %v", err)
	}
	if len(ids) != 1 || ids[0] != legacy.ID {
		t.Fatalf("兼容草稿未成为清扫候选 ids=%v", ids)
	}
}

// 测试目标：验证 000008 会为缺少 rejected_at 的历史拒绝视频回填时间基准
// 预期效果：回填值使用 updated_at，使旧记录也能进入有限保留期清扫
func TestRejectedPurgeMigrationBackfillsLegacyTimestamp(t *testing.T) {
	db := DB(t)
	legacy := &video.Video{
		AuthorID:       1,
		Title:          "legacy rejected video",
		Status:         video.VideoStatusRejected,
		RejectedReason: "历史拒绝",
	}
	if err := db.Create(legacy).Error; err != nil {
		t.Fatalf("创建历史 rejected 视频失败: %v", err)
	}
	updatedAt := time.Now().Add(-2 * time.Hour)
	if err := db.Exec("UPDATE videos SET updated_at = ?, rejected_at = NULL WHERE id = ?", updatedAt, legacy.ID).Error; err != nil {
		t.Fatalf("准备缺少 rejected_at 的历史记录失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(migrationsDir(), "000008_rejected_purge_index.up.sql"))
	if err != nil {
		t.Fatalf("读取 000008 迁移失败: %v", err)
	}
	updateSQL := migrationUpdateStatement(string(content))
	if updateSQL == "" {
		t.Fatal("000008 迁移缺少 rejected_at 回填语句")
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		t.Fatalf("执行 rejected_at 回填失败: %v", err)
	}

	var got videoTimestamp
	if err := db.Raw("SELECT rejected_at, updated_at FROM videos WHERE id = ?", legacy.ID).Scan(&got).Error; err != nil {
		t.Fatalf("读取回填后的历史记录失败: %v", err)
	}
	if got.RejectedAt == nil || !got.RejectedAt.Equal(got.UpdatedAt) {
		t.Fatalf("rejected_at 回填错误 got=%+v want updated_at=%v", got, got.UpdatedAt)
	}
}

// 测试目标：验证 000009 迁移把历史 pending 事件的 next_attempt_at 回填为 created_at
// 预期效果：迁移前的 pending 行立即可被 claim，非 pending 行不被回填
func TestOutboxLeaseMigrationBackfillsLegacyPending(t *testing.T) {
	db := DB(t)
	row := &video.Video{
		AuthorID: 1,
		Title:    "outbox 回填视频",
		Status:   video.VideoStatusProcessing,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("创建处理视频失败: %v", err)
	}
	pending := &video.OutboxEvent{
		EventID:   "evt-backfill-pending",
		VideoID:   row.ID,
		EventType: video.VideoProcessEventType,
		Status:    video.OutboxEventStatusPending,
	}
	dispatched := &video.OutboxEvent{
		EventID:   "evt-backfill-dispatched",
		VideoID:   row.ID,
		EventType: video.VideoProcessEventType,
		Status:    video.OutboxEventStatusDispatched,
	}
	for _, event := range []*video.OutboxEvent{pending, dispatched} {
		if err := db.Create(event).Error; err != nil {
			t.Fatalf("创建 outbox 事件失败: %v", err)
		}
	}
	// 测试目标：还原迁移前的历史状态
	// 预期效果：两条事件都不带 next_attempt_at
	if err := db.Exec("UPDATE video_outbox_events SET next_attempt_at = NULL WHERE id IN (?, ?)", pending.ID, dispatched.ID).Error; err != nil {
		t.Fatalf("还原历史状态失败: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(migrationsDir(), "000009_outbox_publishing_lease.up.sql"))
	if err != nil {
		t.Fatalf("读取 000009 迁移失败: %v", err)
	}
	updateSQL := migrationUpdateStatementFor(string(content), "UPDATE video_outbox_events")
	if updateSQL == "" {
		t.Fatal("000009 迁移缺少 next_attempt_at 回填语句")
	}
	if err := db.Exec(updateSQL).Error; err != nil {
		t.Fatalf("执行 next_attempt_at 回填失败: %v", err)
	}

	var backfilled struct {
		NextAttemptAt *time.Time `gorm:"column:next_attempt_at"`
		CreatedAt     time.Time  `gorm:"column:created_at"`
	}
	if err := db.Raw("SELECT next_attempt_at, created_at FROM video_outbox_events WHERE id = ?", pending.ID).Scan(&backfilled).Error; err != nil {
		t.Fatalf("读取回填后的 pending 行失败: %v", err)
	}
	if backfilled.NextAttemptAt == nil || !backfilled.NextAttemptAt.Equal(backfilled.CreatedAt) {
		t.Fatalf("pending 行应回填为 created_at got=%+v want created_at=%v", backfilled, backfilled.CreatedAt)
	}

	var untouched struct {
		NextAttemptAt *time.Time `gorm:"column:next_attempt_at"`
	}
	if err := db.Raw("SELECT next_attempt_at FROM video_outbox_events WHERE id = ?", dispatched.ID).Scan(&untouched).Error; err != nil {
		t.Fatalf("读取 dispatched 行失败: %v", err)
	}
	if untouched.NextAttemptAt != nil {
		t.Fatalf("非 pending 行不应被回填 got=%v", untouched.NextAttemptAt)
	}
}

type videoTimestamp struct {
	RejectedAt *time.Time `gorm:"column:rejected_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
}

func migrationUpdateStatement(content string) string {
	return migrationUpdateStatementFor(content, "UPDATE videos")
}

// 测试目标：从迁移脚本中抽取指定表的更新语句
// 预期效果：回填用例复用迁移自身的语句而不复制 SQL 文本
func migrationUpdateStatementFor(content, prefix string) string {
	for _, statement := range strings.Split(content, ";") {
		if index := strings.Index(statement, prefix); index >= 0 {
			return strings.TrimSpace(statement[index:])
		}
	}
	return ""
}

func draftPurgeCompatibilityUpdate(content string) string {
	return migrationUpdateStatement(content)
}

// expectedHighestMigrationVersion 是源码迁移目录必须达到的最高版本号
const expectedHighestMigrationVersion = 10

// migrationFileNamePattern 约束迁移文件使用六位补零版本号与方向后缀
var migrationFileNamePattern = regexp.MustCompile(`^(\d{6})_([a-z0-9_]+)\.(up|down)\.sql$`)

// migrationStatementPatterns 静态抽取单条迁移语句操作的对象名
var migrationStatementPatterns = map[string]*regexp.Regexp{
	"createTable":  regexp.MustCompile(`(?i)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?([a-z_]+)`),
	"dropTable":    regexp.MustCompile(`(?i)DROP\s+TABLE\s+(?:IF\s+EXISTS\s+)?([a-z_]+)`),
	"addColumn":    regexp.MustCompile(`(?i)ADD\s+COLUMN\s+([a-z_]+)`),
	"dropColumn":   regexp.MustCompile(`(?i)DROP\s+COLUMN\s+([a-z_]+)`),
	"modifyColumn": regexp.MustCompile(`(?i)MODIFY\s+COLUMN\s+([a-z_]+)`),
	"addIndex":     regexp.MustCompile(`(?i)ADD\s+INDEX\s+([a-z_]+)`),
	"dropIndex":    regexp.MustCompile(`(?i)DROP\s+INDEX\s+([a-z_]+)`),
}

// migrationInverseStatement 声明同一版本内 down 必须出现的互逆操作
var migrationInverseStatement = map[string][]string{
	"createTable":  {"dropTable"},
	"dropTable":    {"createTable"},
	"addColumn":    {"dropColumn"},
	"dropColumn":   {"addColumn"},
	"modifyColumn": {"modifyColumn"},
	"addIndex":     {"dropIndex"},
	"dropIndex":    {"addIndex"},
}

// migrationFile 保存单个迁移文件的解析结果
type migrationFile struct {
	name    string
	version int
	stem    string
	up      bool
}

// migrationVersionPair 保存单个版本的两个方向
type migrationVersionPair struct {
	up   migrationFile
	down migrationFile
}

// expectedColumn 描述迁移声明的单个列定义，空字段表示不校验该维度
type expectedColumn struct {
	name         string
	columnType   string
	nullable     string
	defaultValue *string
	charset      string
}

// expectedIndex 描述迁移声明的单个索引定义，descending 使用一基列位置
type expectedIndex struct {
	name       string
	columns    []string
	unique     bool
	descending map[int]bool
}

// stringPtr 返回字符串指针，用于表达列默认值
func stringPtr(value string) *string {
	return &value
}

// 测试目标：验证迁移版本号连续成对且最高版本等于源码声明值
// 预期效果：十个版本各自提供 up 与 down，无缺号无重复，最高版本为 000010_interaction_outbox
func TestMigrationFilesArePairedAndContiguous(t *testing.T) {
	files := readMigrationFiles(t)
	pairs := groupMigrationFiles(files)
	versions := sortedMigrationVersions(pairs)

	if len(versions) != expectedHighestMigrationVersion {
		t.Errorf("迁移版本数量错误 got=%d want=%d versions=%v", len(versions), expectedHighestMigrationVersion, versions)
	}
	for index, version := range versions {
		if version != index+1 {
			t.Errorf("迁移版本号不连续，第 %d 个版本为 %06d versions=%v", index+1, version, versions)
		}
		pair := pairs[version]
		if pair.up.name == "" || pair.down.name == "" {
			t.Errorf("版本 %06d 未成对提供 up 与 down up=%q down=%q", version, pair.up.name, pair.down.name)
			continue
		}
		if pair.up.stem != pair.down.stem {
			t.Errorf("版本 %06d 的 up 与 down 名称不一致 up=%q down=%q", version, pair.up.stem, pair.down.stem)
		}
	}
	if len(files) != expectedHighestMigrationVersion*2 {
		t.Errorf("迁移文件数量错误 got=%d want=%d", len(files), expectedHighestMigrationVersion*2)
	}

	highest := pairs[expectedHighestMigrationVersion]
	if highest.up.stem != "interaction_outbox" || highest.down.stem != "interaction_outbox" {
		t.Errorf("最高版本名称错误 got up=%q down=%q", highest.up.stem, highest.down.stem)
	}

	for _, file := range files {
		if !containsExecutableStatement(readMigrationContent(t, file.name)) {
			t.Errorf("迁移文件 %s 不含可执行语句", file.name)
		}
	}
}

// 测试目标：验证临时测试库命名随机且报告共享服务端上的残留临时库
// 预期效果：库名派生自业务库名并绑定当前进程与纳秒时间戳，残留库只报告不失败
func TestTemporaryDatabaseIsRandomAndNotLeftBehind(t *testing.T) {
	if testDB == nil || testDBName == "" {
		t.Skip("需要真实 MySQL：设置 MYSQL_DATABASE（以及 MYSQL_HOST、MYSQL_ROOT_PASSWORD 等）后重跑")
	}
	cfg := envConfig()
	prefix := cfg.DBName + "_test_"
	if !strings.HasPrefix(testDBName, prefix) {
		t.Fatalf("临时库命名未派生自业务库名 got=%q prefix=%q", testDBName, prefix)
	}
	parts := strings.Split(strings.TrimPrefix(testDBName, prefix), "_")
	if len(parts) != 2 {
		t.Fatalf("临时库后缀不是进程号与时间戳 got=%q", testDBName)
	}
	pid, err := strconv.Atoi(parts[0])
	if err != nil || pid != os.Getpid() {
		t.Errorf("临时库未绑定当前进程 got=%q pid=%d err=%v", testDBName, os.Getpid(), err)
	}
	if stamp, err := strconv.ParseInt(parts[1], 10, 64); err != nil || stamp <= 0 {
		t.Errorf("临时库缺少纳秒时间戳后缀 got=%q err=%v", testDBName, err)
	}

	var schemas []string
	if err := testDB.Raw(`
		SELECT SCHEMA_NAME FROM information_schema.SCHEMATA
		WHERE SCHEMA_NAME NOT IN ('information_schema', 'mysql', 'performance_schema', 'sys')
		ORDER BY SCHEMA_NAME
	`).Scan(&schemas).Error; err != nil {
		t.Fatalf("读取库清单失败: %v", err)
	}
	// 共享服务端上并存并发测试进程的临时库，只报告数量与名称而不据此判定失败
	leftovers := make([]string, 0, len(schemas))
	for _, schema := range schemas {
		if strings.HasPrefix(schema, prefix) && schema != testDBName {
			leftovers = append(leftovers, schema)
		}
	}
	t.Logf("[临时库] 本次进程库名=%s", testDBName)
	if len(leftovers) != 0 {
		t.Logf("[临时库差异] 服务端存在 %d 个其它进程的临时库: %v", len(leftovers), leftovers)
	}
}

// 测试目标：验证每个版本的 up 与 down 操作对象严格互逆
// 预期效果：建表对删表、加列对删列、加索引对删索引、改列对改列，不留下无法回滚的单向操作
func TestMigrationVersionsAreReversible(t *testing.T) {
	files := readMigrationFiles(t)
	pairs := groupMigrationFiles(files)

	for _, version := range sortedMigrationVersions(pairs) {
		pair := pairs[version]
		if pair.up.name == "" || pair.down.name == "" {
			continue
		}
		upOperations := migrationStatementObjects(t, pair.up.name)
		downOperations := migrationStatementObjects(t, pair.down.name)
		for kind, inverses := range migrationInverseStatement {
			for _, inverse := range inverses {
				if sameStringSet(upOperations[kind], downOperations[inverse]) {
					continue
				}
				t.Errorf(
					"版本 %06d 的 %s 与 down 的 %s 不互逆 up=%v down=%v",
					version, kind, inverse, upOperations[kind], downOperations[inverse],
				)
			}
		}
	}
}

// readMigrationFiles 解析迁移目录中的全部迁移文件
// 命名不符六位版本号规范时立即失败，避免版本清单被静默跳过
func readMigrationFiles(t *testing.T) []migrationFile {
	t.Helper()
	entries, err := os.ReadDir(migrationsDir())
	if err != nil {
		t.Fatalf("读取迁移目录失败: %v", err)
	}

	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		match := migrationFileNamePattern.FindStringSubmatch(entry.Name())
		if match == nil {
			t.Fatalf("迁移文件 %s 命名不符合六位版本号加方向后缀的规范", entry.Name())
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatalf("解析迁移文件 %s 的版本号失败: %v", entry.Name(), err)
		}
		files = append(files, migrationFile{
			name:    entry.Name(),
			version: version,
			stem:    match[2],
			up:      match[3] == "up",
		})
	}
	if len(files) == 0 {
		t.Fatalf("迁移目录 %s 没有迁移文件", migrationsDir())
	}
	// 与测试工具应用迁移时的文件名排序保持一致，预期字典序即版本序
	sort.Slice(files, func(first, second int) bool { return files[first].name < files[second].name })
	return files
}

// groupMigrationFiles 按版本号归并 up 与 down
func groupMigrationFiles(files []migrationFile) map[int]migrationVersionPair {
	pairs := make(map[int]migrationVersionPair, len(files))
	for _, file := range files {
		pair := pairs[file.version]
		if file.up {
			pair.up = file
		} else {
			pair.down = file
		}
		pairs[file.version] = pair
	}
	return pairs
}

// sortedMigrationVersions 返回升序的版本号清单
func sortedMigrationVersions(pairs map[int]migrationVersionPair) []int {
	versions := make([]int, 0, len(pairs))
	for version := range pairs {
		versions = append(versions, version)
	}
	sort.Ints(versions)
	return versions
}

// readMigrationContent 读取单个迁移文件的文本
func readMigrationContent(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(migrationsDir(), name))
	if err != nil {
		t.Fatalf("读取迁移 %s 失败: %v", name, err)
	}
	return string(content)
}

// migrationStatementObjects 抽取单个迁移文件里各操作涉及的对象名
func migrationStatementObjects(t *testing.T, name string) map[string][]string {
	t.Helper()
	content := readMigrationContent(t, name)
	objects := make(map[string][]string, len(migrationStatementPatterns))
	for kind, pattern := range migrationStatementPatterns {
		for _, match := range pattern.FindAllStringSubmatch(content, -1) {
			objects[kind] = append(objects[kind], match[1])
		}
	}
	return objects
}

// containsExecutableStatement 判断迁移文件在去掉注释后是否还有语句
func containsExecutableStatement(content string) bool {
	var body strings.Builder
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}
		body.WriteString(trimmed)
	}
	return strings.Contains(body.String(), ";")
}

// sameStringSet 比较两个字符串集合是否完全一致
func sameStringSet(first, second []string) bool {
	left := append([]string{}, first...)
	right := append([]string{}, second...)
	sort.Strings(left)
	sort.Strings(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// subtractStringSet 返回 first 中存在而 second 中缺失的元素
func subtractStringSet(first, second []string) []string {
	present := make(map[string]bool, len(second))
	for _, item := range second {
		present[item] = true
	}
	missing := make([]string, 0, len(first))
	for _, item := range first {
		if !present[item] {
			missing = append(missing, item)
		}
	}
	sort.Strings(missing)
	return missing
}

// compareColumn 返回期望列定义与实际定义的差异描述
func compareColumn(want expectedColumn, got actualColumn) []string {
	differences := make([]string, 0, 4)
	if want.columnType != "" && want.columnType != got.ColumnType {
		differences = append(differences, fmt.Sprintf("类型为 %s 期望 %s", got.ColumnType, want.columnType))
	}
	if want.nullable != "" && want.nullable != got.IsNullable {
		differences = append(differences, fmt.Sprintf("可空性为 %s 期望 %s", got.IsNullable, want.nullable))
	}
	if want.defaultValue != nil {
		if got.ColumnDefault == nil {
			differences = append(differences, fmt.Sprintf("默认值为 NULL 期望 %q", *want.defaultValue))
		} else if *got.ColumnDefault != *want.defaultValue {
			differences = append(differences, fmt.Sprintf("默认值为 %q 期望 %q", *got.ColumnDefault, *want.defaultValue))
		}
	}
	if want.charset != "" {
		actualCharset := ""
		if got.Charset != nil {
			actualCharset = *got.Charset
		}
		if actualCharset != want.charset {
			differences = append(differences, fmt.Sprintf("字符集为 %q 期望 %q", actualCharset, want.charset))
		}
	}
	return differences
}
