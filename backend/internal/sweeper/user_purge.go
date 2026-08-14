
package sweeper

import (
	"context"
	"errors"
	"time"
)

var ErrUserPurgerUnavailable = errors.New("user purger unavailable")

// UserPurger 是注销用户清扫任务所需的仓储能力子集
type UserPurger interface {
	PurgeExpired(ctx context.Context, cutoff time.Time) (int64, error)
}

// UserPurgeJob 周期性地硬删除超过保留期的注销用户
type UserPurgeJob struct {
	purger    UserPurger
	retention time.Duration
	now       func() time.Time
}

// NewUserPurgeJob 构造清扫任务，retention 表示注销后保留时长
func NewUserPurgeJob(purger UserPurger, retention time.Duration) *UserPurgeJob {
	return &UserPurgeJob{
		purger:    purger,
		retention: retention,
		now:       time.Now,
	}
}

// Run 执行一次清扫并返回硬删除的用户数
func (j *UserPurgeJob) Run(ctx context.Context) (int64, error) {
	if j.purger == nil {
		return 0, ErrUserPurgerUnavailable
	}
	return j.purger.PurgeExpired(ctx, j.now().Add(-j.retention))
}
