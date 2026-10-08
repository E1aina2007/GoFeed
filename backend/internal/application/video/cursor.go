package video

import (
	"encoding/base64"
	"encoding/json"
	"time"

	domainvideo "gofeed/internal/domain/video"
)

const (
	DefaultListLimit     = 20
	MaxListLimit         = 50
	currentCursorVersion = 1
)

type cursorScope struct {
	kind     cursorKind
	authorID uint
}

type cursorKind string

const (
	cursorKindPublic cursorKind = "public"
	cursorKindAuthor cursorKind = "author"
	cursorKindMine   cursorKind = "mine"
)

// cursor 记录列表分页位置及其版本、查询范围
type cursor struct {
	Version     int        `json:"v"`
	Kind        cursorKind `json:"k"`
	AuthorID    uint       `json:"a,omitempty"`
	PublishedAt time.Time  `json:"p"`
	ID          uint       `json:"i"`
}

func publicCursorScope(authorID uint) cursorScope {
	if authorID == 0 {
		return cursorScope{kind: cursorKindPublic}
	}
	return cursorScope{kind: cursorKindAuthor, authorID: authorID}
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 0 || limit > MaxListLimit {
		return 0, domainvideo.ErrInvalidLimit
	}
	return limit, nil
}

func encodeCursor(cursor *cursor) (string, error) {
	if !validCursorFields(cursor) {
		return "", domainvideo.ErrInvalidCursor
	}

	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeCursor(encoded string) (*cursor, error) {
	if encoded == "" {
		return nil, nil
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return nil, domainvideo.ErrInvalidCursor
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil {
		return nil, domainvideo.ErrInvalidCursor
	}
	for field := range fields {
		switch field {
		case "v", "k", "a", "p", "i":
		default:
			return nil, domainvideo.ErrInvalidCursor
		}
	}

	var cursor cursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return nil, domainvideo.ErrInvalidCursor
	}
	if cursor.Kind == cursorKindPublic {
		if _, ok := fields["a"]; ok {
			return nil, domainvideo.ErrInvalidCursor
		}
	} else if _, ok := fields["a"]; !ok {
		return nil, domainvideo.ErrInvalidCursor
	}
	if !validCursorFields(&cursor) {
		return nil, domainvideo.ErrInvalidCursor
	}
	return &cursor, nil
}

func validCursorFields(cursor *cursor) bool {
	if cursor == nil || cursor.Version != currentCursorVersion || cursor.ID == 0 || cursor.PublishedAt.IsZero() {
		return false
	}
	switch cursor.Kind {
	case cursorKindPublic:
		return cursor.AuthorID == 0
	case cursorKindAuthor, cursorKindMine:
		return cursor.AuthorID != 0
	default:
		return false
	}
}

func validateCursorScope(cursor *cursor, scope cursorScope) error {
	if cursor == nil {
		return nil
	}
	if cursor.Kind != scope.kind || cursor.AuthorID != scope.authorID {
		return domainvideo.ErrInvalidCursor
	}
	return nil
}
