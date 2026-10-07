package applicationaccount

import (
	"context"
	"strconv"

	domainaccount "gofeed/internal/domain/account"
)

type Service struct {
	reader        domainaccount.Reader
	videoCounter  domainaccount.PublishedVideoCounter
	metricsReader domainaccount.ProfileMetricsReader
}

func New(reader domainaccount.Reader, videoCounter domainaccount.PublishedVideoCounter, metricsReaders ...domainaccount.ProfileMetricsReader) *Service {
	var metricsReader domainaccount.ProfileMetricsReader
	if len(metricsReaders) > 0 {
		metricsReader = metricsReaders[0]
	}
	return &Service{reader: reader, videoCounter: videoCounter, metricsReader: metricsReader}
}

func (s *Service) GetByID(ctx context.Context, id uint) (domainaccount.PublicAccount, error) {
	return s.reader.GetByID(ctx, id)
}

type UserListQuery struct {
	Limit     string
	Cursor    string
	HasLimit  bool
	HasCursor bool
}

type UserListResult struct {
	Users      []domainaccount.PublicAccount
	NextCursor string
}

// GetUserList 仅在分页参数存在时按 ID 正序分页，否则保留全量读取
func (s *Service) GetUserList(ctx context.Context, query UserListQuery) (UserListResult, error) {
	if !query.HasLimit && !query.HasCursor {
		users, err := s.reader.GetUserList(ctx)
		if err != nil {
			return UserListResult{}, err
		}
		return UserListResult{Users: users}, nil
	}

	limit, err := parseUserListLimit(query.Limit, query.HasLimit)
	if err != nil {
		return UserListResult{}, err
	}
	position, err := decodeUserCursor(query.Cursor)
	if err != nil {
		return UserListResult{}, err
	}
	users, err := s.reader.GetUserListPage(ctx, position, limit+1)
	if err != nil {
		return UserListResult{}, err
	}

	result := UserListResult{Users: users}
	if len(users) <= limit {
		return result, nil
	}
	result.Users = users[:limit]
	result.NextCursor, err = encodeUserCursor(&userCursor{
		Version: userListCursorVersion,
		Kind:    userListCursorKind,
		ID:      result.Users[len(result.Users)-1].ID,
	})
	if err != nil {
		return UserListResult{}, err
	}
	return result, nil
}

// GetProfile 先读取账户和完整公开视频数，再读取可选的实时资料统计
func (s *Service) GetProfile(ctx context.Context, id uint) (domainaccount.Profile, error) {
	account, err := s.reader.GetByID(ctx, id)
	if err != nil {
		return domainaccount.Profile{}, err
	}
	if s.videoCounter == nil {
		return domainaccount.Profile{}, domainaccount.ErrVideoCounterUnavailable
	}
	videoCount, err := s.videoCounter.GetPublishedVideoCountByAuthor(ctx, id)
	if err != nil {
		return domainaccount.Profile{}, err
	}
	profile := domainaccount.Profile{Account: account, VideoCount: videoCount}
	if s.metricsReader == nil {
		return profile, nil
	}
	metrics, err := s.metricsReader.GetProfileMetrics(ctx, id)
	if err != nil {
		return domainaccount.Profile{}, err
	}
	profile.TotalLikes = metrics.TotalLikes
	profile.FollowerCount = metrics.FollowerCount
	profile.VloggerCount = metrics.VloggerCount
	return profile, nil
}

func parseUserListLimit(raw string, supplied bool) (int, error) {
	if !supplied {
		return defaultUserListLimit, nil
	}
	if raw == "" {
		return 0, domainaccount.ErrInvalidUserListLimit
	}
	limit, err := strconv.Atoi(raw)
	if err != nil {
		return 0, domainaccount.ErrInvalidUserListLimit
	}
	if limit < 1 || limit > maxUserListLimit {
		return 0, domainaccount.ErrInvalidUserListLimit
	}
	return limit, nil
}
