package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gorm.io/gorm"
)

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

// expectedMigrationTables 是全部 up 迁移应当创建的业务表集合
var expectedMigrationTables = []string{
	"auth_sessions", "interaction_outbox_events", "user_follows", "users", "video_comments",
	"video_likes", "video_outbox_events", "videos",
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

// 测试目标：验证全部 up 迁移声明的数据表与源码期望集合一致
// 预期效果：迁移产物恰好是七张业务表，既无遗漏建表也无额外建表
func TestMigrationsDeclareExpectedTableSet(t *testing.T) {
	declared := make([]string, 0, len(expectedMigrationTables))
	for _, file := range readMigrationFiles(t) {
		if !file.up {
			continue
		}
		declared = append(declared, migrationStatementObjects(t, file.name)["createTable"]...)
	}
	if !sameStringSet(declared, expectedMigrationTables) {
		t.Fatalf("up 迁移声明的表集合与期望不一致 declared=%v want=%v", declared, expectedMigrationTables)
	}
}

// 测试目标：只读核对业务库的迁移版本账本与对象差异
// 预期效果：默认跳过，显式开启后仅输出差异报告而不修改业务库
func TestBusinessDatabaseSchemaReadOnlyReport(t *testing.T) {
	if os.Getenv("GOFEED_BUSINESS_DB_REPORT") != "1" {
		t.Skip("只读业务库核对需显式开启：设置 GOFEED_BUSINESS_DB_REPORT=1 后重跑")
	}
	cfg := envConfig()
	if cfg.DBName == "" {
		t.Skip("只读业务库核对需要 MYSQL_DATABASE 指定业务库")
	}
	db, err := openMySQL(cfg, cfg.DBName, false)
	if err != nil {
		t.Fatalf("连接业务库失败 host=%s port=%d database=%s err=%v", cfg.Host, cfg.Port, cfg.DBName, err)
	}
	defer func() {
		if err := closeDB(db); err != nil {
			t.Errorf("关闭业务库连接失败: %v", err)
		}
	}()

	var version string
	if err := db.Raw("SELECT VERSION()").Scan(&version).Error; err != nil {
		t.Fatalf("读取 MySQL 版本失败: %v", err)
	}
	t.Logf("[业务库] 目标库=%s 服务端版本=%s 源码最高迁移版本=%06d", cfg.DBName, version, expectedHighestMigrationVersion)

	reportMigrationLedger(t, db)
	reportTableDifferences(t, db)
	reportColumnDifferences(t, db)
	reportModelColumnSetDifferences(t, db)
	reportIndexDifferences(t, db)
}

// reportModelColumnSetDifferences 输出全部业务表与模型字段的列集合差异
func reportModelColumnSetDifferences(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, target := range modelSchemaTargets {
		parsed := parseModelSchema(t, target)
		actual := readTableColumns(t, db, parsed.Table)
		if len(actual) == 0 {
			t.Logf("[业务库差异] 业务库缺少模型 %T 对应的表 %s", target, parsed.Table)
			continue
		}
		declared := modelColumnNames(parsed)
		physical := tableColumnNames(actual)
		if sameStringSet(declared, physical) {
			continue
		}
		t.Logf(
			"[业务库差异] 表 %s 的模型列与业务库列不一致 业务库缺少=%v 业务库多出=%v",
			parsed.Table, subtractStringSet(declared, physical), subtractStringSet(physical, declared),
		)
	}
}

// reportMigrationLedger 输出业务库 schema_migrations 的版本与 dirty 状态
func reportMigrationLedger(t *testing.T, db *gorm.DB) {
	t.Helper()
	var ledgerExists int64
	if err := db.Raw(`
		SELECT COUNT(*) FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'schema_migrations'
	`).Scan(&ledgerExists).Error; err != nil {
		t.Logf("[业务库差异] 读取 schema_migrations 存在性失败: %v", err)
		return
	}
	if ledgerExists == 0 {
		t.Logf("[业务库差异] schema_migrations 不存在，业务库不是由 golang-migrate 维护")
		return
	}

	var rows []struct {
		Version int64 `gorm:"column:version"`
		Dirty   bool  `gorm:"column:dirty"`
	}
	if err := db.Raw("SELECT version, dirty FROM schema_migrations ORDER BY version").Scan(&rows).Error; err != nil {
		t.Logf("[业务库差异] 读取 schema_migrations 内容失败: %v", err)
		return
	}
	if len(rows) == 0 {
		t.Logf("[业务库差异] schema_migrations 为空表，业务库没有任何迁移版本记录")
		return
	}

	dirtyCount := 0
	for _, row := range rows {
		if row.Dirty {
			dirtyCount++
		}
		t.Logf("[业务库] schema_migrations version=%d dirty=%v", row.Version, row.Dirty)
	}
	current := rows[len(rows)-1]
	if current.Dirty {
		t.Logf("[业务库差异] 账本 dirty=1，版本 %d 的迁移中断，需人工确认后再继续", current.Version)
	}
	if current.Version < int64(expectedHighestMigrationVersion) {
		t.Logf(
			"[业务库差异] 业务库版本落后：实际=%d 源码最高=%d 缺少 %d 个版本",
			current.Version, expectedHighestMigrationVersion, int64(expectedHighestMigrationVersion)-current.Version,
		)
	}
	if current.Version > int64(expectedHighestMigrationVersion) {
		t.Logf("[业务库差异] 业务库版本高于源码：实际=%d 源码最高=%d", current.Version, expectedHighestMigrationVersion)
	}
	if dirtyCount > 1 {
		t.Logf("[业务库差异] schema_migrations 存在 %d 条 dirty 记录", dirtyCount)
	}
}

// reportTableDifferences 输出业务库表集合与源码迁移期望的差异
func reportTableDifferences(t *testing.T, db *gorm.DB) {
	t.Helper()
	actual := readBusinessTables(t)
	// 迁移账本由 golang-migrate 自身创建，不属于任何迁移文件声明的对象
	declared := make([]string, 0, len(actual))
	ledgerPresent := false
	for _, table := range actual {
		if table == migrationLedgerTable {
			ledgerPresent = true
			continue
		}
		declared = append(declared, table)
	}
	if ledgerPresent {
		t.Logf("[业务库] 存在迁移工具自建的账本表 %s，不计入差异", migrationLedgerTable)
	}
	missing := subtractStringSet(expectedMigrationTables, declared)
	extra := subtractStringSet(declared, expectedMigrationTables)
	t.Logf("[业务库] 实际业务表=%v", declared)
	if len(missing) > 0 {
		t.Logf("[业务库差异] 缺少迁移声明的表: %v", missing)
	}
	if len(extra) > 0 {
		t.Logf("[业务库差异] 存在源码迁移未声明的表: %v", extra)
	}

	var collations []struct {
		TableName      string `gorm:"column:TABLE_NAME"`
		TableCollation string `gorm:"column:TABLE_COLLATION"`
	}
	if err := db.Raw(`
		SELECT TABLE_NAME, TABLE_COLLATION FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`).Scan(&collations).Error; err != nil {
		t.Logf("[业务库差异] 读取表字符集失败: %v", err)
		return
	}
	for _, row := range collations {
		if row.TableCollation != expectedTableCollation {
			t.Logf("[业务库差异] 表 %s 排序规则为 %s，期望 %s", row.TableName, row.TableCollation, expectedTableCollation)
		}
	}
}

// reportColumnDifferences 输出公开读取与 outbox 关键列的定义差异
func reportColumnDifferences(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, check := range []struct {
		table   string
		columns []expectedColumn
	}{
		{table: "videos", columns: expectedVideoColumns},
		{table: "video_outbox_events", columns: expectedOutboxColumns},
	} {
		actual := readTableColumns(t, db, check.table)
		if len(actual) == 0 {
			t.Logf("[业务库差异] 表 %s 不存在，无法比较列定义", check.table)
			continue
		}
		for _, want := range check.columns {
			got, ok := actual[want.name]
			if !ok {
				t.Logf("[业务库差异] 表 %s 缺少列 %s", check.table, want.name)
				continue
			}
			for _, diff := range compareColumn(want, got) {
				t.Logf("[业务库差异] 表 %s 列 %s %s", check.table, want.name, diff)
			}
		}
	}
}

// reportIndexDifferences 输出公开读取与 outbox 关键索引的差异
func reportIndexDifferences(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, check := range []struct {
		table   string
		indexes []expectedIndex
	}{
		{table: "videos", indexes: expectedVideoIndexes},
		{table: "video_outbox_events", indexes: expectedOutboxIndexes},
	} {
		actual := readTableIndexes(t, db, check.table)
		if len(actual) == 0 {
			t.Logf("[业务库差异] 表 %s 不存在，无法比较索引定义", check.table)
			continue
		}
		for _, want := range check.indexes {
			got, ok := actual[want.name]
			if !ok {
				t.Logf("[业务库差异] 表 %s 缺少索引 %s", check.table, want.name)
				continue
			}
			if !sameStringSet(got.columns, want.columns) {
				t.Logf("[业务库差异] 表 %s 索引 %s 列为 %v，期望 %v", check.table, want.name, got.columns, want.columns)
			}
			if got.unique != want.unique {
				t.Logf("[业务库差异] 表 %s 索引 %s 唯一性为 %v，期望 %v", check.table, want.name, got.unique, want.unique)
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

// readBusinessTables 读取业务库当前的基础表清单
func readBusinessTables(t *testing.T) []string {
	t.Helper()
	cfg := envConfig()
	db, err := openMySQL(cfg, cfg.DBName, false)
	if err != nil {
		t.Fatalf("连接业务库读取表清单失败: %v", err)
	}
	defer func() {
		if err := closeDB(db); err != nil {
			t.Errorf("关闭业务库连接失败: %v", err)
		}
	}()

	var tables []string
	if err := db.Raw(`
		SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'
		ORDER BY TABLE_NAME
	`).Scan(&tables).Error; err != nil {
		t.Fatalf("读取业务库表清单失败: %v", err)
	}
	return tables
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
