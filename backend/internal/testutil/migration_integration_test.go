package testutil

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"

	"gofeed/internal/auth"
	"gofeed/internal/social"
	"gofeed/internal/user"
	"gofeed/internal/video"

	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// expectedTableCollation 是迁移声明的表排序规则
const expectedTableCollation = "utf8mb4_0900_ai_ci"

// migrationLedgerTable 是 golang-migrate 自身维护的版本账本，不属于迁移声明的业务表
const migrationLedgerTable = "schema_migrations"

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

// actualForeignKey 保存 information_schema 返回的单个外键定义
type actualForeignKey struct {
	ConstraintName  string `gorm:"column:CONSTRAINT_NAME"`
	ColumnName      string `gorm:"column:COLUMN_NAME"`
	ReferencedTable string `gorm:"column:REFERENCED_TABLE_NAME"`
	DeleteRule      string `gorm:"column:DELETE_RULE"`
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

// 测试目标：验证从零应用全部迁移后产生的表集合与排序规则
// 预期效果：恰好存在七张 InnoDB 业务表且排序规则统一为 utf8mb4_0900_ai_ci
func TestMigrationProductCreatesExactTableSet(t *testing.T) {
	db := DB(t)

	var tables []struct {
		TableName      string `gorm:"column:TABLE_NAME"`
		Engine         string `gorm:"column:ENGINE"`
		TableCollation string `gorm:"column:TABLE_COLLATION"`
	}
	if err := db.Raw(`
		SELECT TABLE_NAME, ENGINE, TABLE_COLLATION FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("读取迁移产物的表清单失败: %v", err)
	}
	names := make([]string, 0, len(tables))
	for _, table := range tables {
		names = append(names, table.TableName)
		if table.Engine != "InnoDB" {
			t.Errorf("表 %s 的存储引擎为 %s 期望 InnoDB", table.TableName, table.Engine)
		}
		if table.TableCollation != expectedTableCollation {
			t.Errorf("表 %s 的排序规则为 %s 期望 %s", table.TableName, table.TableCollation, expectedTableCollation)
		}
	}
	if !sameStringSet(names, expectedMigrationTables) {
		t.Fatalf("迁移产物表集合错误 got=%v want=%v", names, expectedMigrationTables)
	}

	// 测试工具按文件集合直接应用迁移，不经 golang-migrate，故本包不覆盖账本与 dirty 语义
	var ledger int64
	if err := db.Raw(`
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'schema_migrations'
	`).Scan(&ledger).Error; err != nil {
		t.Fatalf("读取 schema_migrations 存在性失败: %v", err)
	}
	if ledger != 0 {
		t.Logf("临时库存在 schema_migrations，本包可另行覆盖账本语义")
	}
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

// 测试目标：验证 000006 删除的派生计数值列没有回归
// 预期效果：videos 不含 likes_count 与 comments_count，互动计数只由关系表聚合
func TestMigrationProductDropsDerivedCounterColumns(t *testing.T) {
	db := DB(t)
	actual := readTableColumns(t, db, "videos")
	for _, name := range []string{"likes_count", "comments_count"} {
		if _, ok := actual[name]; ok {
			t.Errorf("000006 已删除的派生计数值列 %s 重新出现在 videos 中", name)
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

// 测试目标：验证互动关系表的唯一约束与外键级联定义
// 预期效果：点赞关注不可重复，父行物理删除时关系行与 outbox 事件级联移除
func TestMigrationProductSocialConstraints(t *testing.T) {
	db := DB(t)

	uniqueChecks := []struct {
		table   string
		columns []string
	}{
		{table: "video_likes", columns: []string{"video_id", "user_id"}},
		{table: "user_follows", columns: []string{"follower_id", "followee_id"}},
		{table: "video_outbox_events", columns: []string{"event_id"}},
		{table: "users", columns: []string{"username"}},
		{table: "auth_sessions", columns: []string{"refresh_token_hash"}},
	}
	for _, check := range uniqueChecks {
		if !hasUniqueIndexOn(readTableIndexes(t, db, check.table), check.columns) {
			t.Errorf("表 %s 缺少列组合 %v 的唯一约束", check.table, check.columns)
		}
	}

	cascadeChecks := []struct {
		table      string
		column     string
		referenced string
	}{
		{table: "video_likes", column: "video_id", referenced: "videos"},
		{table: "video_likes", column: "user_id", referenced: "users"},
		{table: "user_follows", column: "follower_id", referenced: "users"},
		{table: "user_follows", column: "followee_id", referenced: "users"},
		{table: "video_comments", column: "video_id", referenced: "videos"},
		{table: "video_comments", column: "author_id", referenced: "users"},
		{table: "video_outbox_events", column: "video_id", referenced: "videos"},
	}
	for _, check := range cascadeChecks {
		foreignKey, ok := readForeignKeys(t, db, check.table)[check.column]
		if !ok {
			t.Errorf("表 %s 的列 %s 缺少外键", check.table, check.column)
			continue
		}
		if foreignKey.ReferencedTable != check.referenced || foreignKey.DeleteRule != "CASCADE" {
			t.Errorf(
				"表 %s 的列 %s 外键为 %s ON DELETE %s 期望 %s ON DELETE CASCADE",
				check.table, check.column, foreignKey.ReferencedTable, foreignKey.DeleteRule, check.referenced,
			)
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

// readForeignKeys 读取指定表的外键定义并按本地列名归并
func readForeignKeys(t *testing.T, db *gorm.DB, table string) map[string]actualForeignKey {
	t.Helper()
	var rows []actualForeignKey
	if err := db.Raw(`
		SELECT k.CONSTRAINT_NAME, k.COLUMN_NAME, k.REFERENCED_TABLE_NAME, r.DELETE_RULE
		FROM information_schema.KEY_COLUMN_USAGE k
		JOIN information_schema.REFERENTIAL_CONSTRAINTS r
		  ON r.CONSTRAINT_SCHEMA = k.CONSTRAINT_SCHEMA
		 AND r.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		 AND r.TABLE_NAME = k.TABLE_NAME
		WHERE k.TABLE_SCHEMA = DATABASE() AND k.TABLE_NAME = ? AND k.REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY k.COLUMN_NAME
	`, table).Scan(&rows).Error; err != nil {
		t.Fatalf("读取表 %s 的外键元数据失败: %v", table, err)
	}
	foreignKeys := make(map[string]actualForeignKey, len(rows))
	for _, row := range rows {
		foreignKeys[row.ColumnName] = row
	}
	return foreignKeys
}

// hasUniqueIndexOn 判断是否存在列顺序完全一致且唯一的索引
func hasUniqueIndexOn(indexes map[string]actualIndex, columns []string) bool {
	for _, index := range indexes {
		if index.unique && sameStringSequence(index.columns, columns) {
			return true
		}
	}
	return false
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
