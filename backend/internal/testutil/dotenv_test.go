package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// 测试目标：验证加载 .env 时补充缺失变量且不覆盖已导出的环境变量
// 预期效果：仅存在于文件中的变量生效，进程中已有的变量保持原值
func TestLoadDotEnvKeepsExistingEnvironment(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "TESTUTIL_DOTENV_PROBE=from-file\nTESTUTIL_DOTENV_PROBE_PRESET=from-file\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写入临时 .env 失败: %v", err)
	}
	t.Setenv("TESTUTIL_DOTENV_PROBE_PRESET", "from-env")
	t.Cleanup(func() { os.Unsetenv("TESTUTIL_DOTENV_PROBE") })

	loadDotEnv(path)

	if got := os.Getenv("TESTUTIL_DOTENV_PROBE"); got != "from-file" {
		t.Errorf("文件内变量应被加载 got=%q", got)
	}
	if got := os.Getenv("TESTUTIL_DOTENV_PROBE_PRESET"); got != "from-env" {
		t.Errorf("已导出的环境变量不应被覆盖 got=%q", got)
	}
}
