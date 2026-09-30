package user

import (
	"encoding/base64"
	"encoding/json"
)

const (
	defaultUserListLimit  = 20
	maxUserListLimit      = 50
	userListCursorVersion = 1
	userListCursorKind    = "users"
)

func normalizeUserListLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultUserListLimit, nil
	}
	if limit < 1 || limit > maxUserListLimit {
		return 0, ErrInvalidUserListLimit
	}
	return limit, nil
}

func encodeUserCursor(cursor *UserCursor) (string, error) {
	if !validUserCursor(cursor) {
		return "", ErrInvalidUserCursor
	}

	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeUserCursor(raw string) (*UserCursor, error) {
	if raw == "" {
		return nil, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, ErrInvalidUserCursor
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || len(fields) != 3 {
		return nil, ErrInvalidUserCursor
	}
	for field := range fields {
		switch field {
		case "v", "k", "i":
		default:
			return nil, ErrInvalidUserCursor
		}
	}

	var cursor UserCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || !validUserCursor(&cursor) {
		return nil, ErrInvalidUserCursor
	}
	return &cursor, nil
}

func validUserCursor(cursor *UserCursor) bool {
	return cursor != nil && cursor.Version == userListCursorVersion && cursor.Kind == userListCursorKind && cursor.ID != 0
}
