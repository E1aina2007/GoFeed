package applicationaccount

import (
	"encoding/base64"
	"encoding/json"

	domainaccount "gofeed/internal/domain/account"
)

const (
	defaultUserListLimit  = 20
	maxUserListLimit      = 50
	userListCursorVersion = 1
	userListCursorKind    = "users"
)

// userCursor 保留原 v1 RawURL Base64 的字段及编码顺序
type userCursor struct {
	Version int    `json:"v"`
	Kind    string `json:"k"`
	ID      uint   `json:"i"`
}

func encodeUserCursor(cursor *userCursor) (string, error) {
	if !validUserCursor(cursor) {
		return "", domainaccount.ErrInvalidUserCursor
	}

	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeUserCursor(raw string) (*domainaccount.ListPosition, error) {
	if raw == "" {
		return nil, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, domainaccount.ErrInvalidUserCursor
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || len(fields) != 3 {
		return nil, domainaccount.ErrInvalidUserCursor
	}
	for field := range fields {
		switch field {
		case "v", "k", "i":
		default:
			return nil, domainaccount.ErrInvalidUserCursor
		}
	}

	var cursor userCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !validUserCursor(&cursor) {
		return nil, domainaccount.ErrInvalidUserCursor
	}
	return &domainaccount.ListPosition{ID: cursor.ID}, nil
}

func validUserCursor(cursor *userCursor) bool {
	return cursor != nil && cursor.Version == userListCursorVersion && cursor.Kind == userListCursorKind && cursor.ID != 0
}
