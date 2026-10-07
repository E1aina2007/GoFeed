package applicationrelation

import (
	"encoding/base64"
	"encoding/json"
	"time"

	domainrelation "gofeed/internal/domain/relation"
)

const (
	DefaultListLimit    = 20
	MaxListLimit        = 50
	cursorKindFollowers = "followers"
	cursorKindFollowing = "following"
)

// followCursor 保留旧客户端持有的 v1 RawURL Base64 字段与编码顺序
type followCursor struct {
	Version   int       `json:"v"`
	Kind      string    `json:"k"`
	UserID    uint      `json:"r"`
	CreatedAt time.Time `json:"p"`
	ID        uint      `json:"i"`
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, domainrelation.ErrInvalidLimit
	}
	return limit, nil
}

func decodeFollowCursor(raw, kind string, userID uint) (*domainrelation.FollowPosition, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, domainrelation.ErrInvalidCursor
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || len(fields) != 5 {
		return nil, domainrelation.ErrInvalidCursor
	}
	for _, key := range []string{"v", "k", "r", "p", "i"} {
		if _, ok := fields[key]; !ok {
			return nil, domainrelation.ErrInvalidCursor
		}
	}
	var cursor followCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !cursor.valid() || cursor.Kind != kind || cursor.UserID != userID {
		return nil, domainrelation.ErrInvalidCursor
	}
	return &domainrelation.FollowPosition{CreatedAt: cursor.CreatedAt, ID: cursor.ID}, nil
}

func encodeFollowCursor(kind string, userID uint, position domainrelation.FollowPosition) (string, error) {
	cursor := followCursor{Version: 1, Kind: kind, UserID: userID, CreatedAt: position.CreatedAt, ID: position.ID}
	if !cursor.valid() {
		return "", domainrelation.ErrInvalidCursor
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (c followCursor) valid() bool {
	return c.Version == 1 && (c.Kind == cursorKindFollowers || c.Kind == cursorKindFollowing) &&
		c.UserID != 0 && !c.CreatedAt.IsZero() && c.ID != 0
}
