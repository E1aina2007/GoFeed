package applicationinteraction

import (
	"encoding/base64"
	"encoding/json"
	"time"

	domaininteraction "gofeed/internal/domain/interaction"
)

const (
	DefaultListLimit = 20
	MaxListLimit     = 50
)

// commentCursor 保留旧客户端持有的 v1 RawURL Base64 字段与编码顺序
type commentCursor struct {
	Version   int       `json:"v"`
	Kind      string    `json:"k"`
	VideoID   uint      `json:"r"`
	CreatedAt time.Time `json:"p"`
	ID        uint      `json:"i"`
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, domaininteraction.ErrInvalidLimit
	}
	return limit, nil
}

func decodeCommentCursor(raw string, videoID uint) (*domaininteraction.CommentPosition, error) {
	if raw == "" {
		return nil, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, domaininteraction.ErrInvalidCursor
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || len(fields) != 5 {
		return nil, domaininteraction.ErrInvalidCursor
	}
	for _, key := range []string{"v", "k", "r", "p", "i"} {
		if _, ok := fields[key]; !ok {
			return nil, domaininteraction.ErrInvalidCursor
		}
	}
	var cursor commentCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !cursor.valid() || cursor.VideoID != videoID {
		return nil, domaininteraction.ErrInvalidCursor
	}
	return &domaininteraction.CommentPosition{CreatedAt: cursor.CreatedAt, ID: cursor.ID}, nil
}

func encodeCommentCursor(videoID uint, position domaininteraction.CommentPosition) (string, error) {
	cursor := commentCursor{Version: 1, Kind: "comments", VideoID: videoID, CreatedAt: position.CreatedAt, ID: position.ID}
	if !cursor.valid() {
		return "", domaininteraction.ErrInvalidCursor
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (c commentCursor) valid() bool {
	return c.Version == 1 && c.Kind == "comments" && c.VideoID != 0 && !c.CreatedAt.IsZero() && c.ID != 0
}
