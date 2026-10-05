package testutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// actualColumn 保存 information_schema 返回的单个列定义
type actualColumn struct {
	ColumnName    string  `gorm:"column:COLUMN_NAME"`
	ColumnType    string  `gorm:"column:COLUMN_TYPE"`
	IsNullable    string  `gorm:"column:IS_NULLABLE"`
	ColumnDefault *string `gorm:"column:COLUMN_DEFAULT"`
	Charset       *string `gorm:"column:CHARACTER_SET_NAME"`
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

// expectedColumn 描述迁移声明的单个列定义，空字段表示不校验该维度
type expectedColumn struct {
	name         string
	columnType   string
	nullable     string
	defaultValue *string
	charset      string
}

// stringPtr 返回字符串指针，用于表达列默认值
func stringPtr(value string) *string {
	return &value
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
