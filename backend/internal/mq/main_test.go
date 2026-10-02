package mq

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/joho/godotenv"
)

// 测试目标：测试进程启动时补充加载 backend/.env 的集成测试配置
// 预期效果：真实 RabbitMQ 集成用例在标准测试命令下不再因缺少环境变量而跳过，已注入的环境变量优先级不变
func TestMain(m *testing.M) {
	loadBackendDotEnv()
	os.Exit(m.Run())
}

// 测试目标：通过测试源文件位置定位 backend/.env
// 预期效果：不依赖测试进程工作目录即可找到配置文件，文件缺失时静默跳过并保留原有跳过行为
func loadBackendDotEnv() {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return
	}
	// CI 通过显式环境变量注入配置，缺失文件属正常场景
	_ = godotenv.Load(filepath.Join(filepath.Dir(file), "..", "..", ".env"))
}
