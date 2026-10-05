package applicationfeed

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	domainfeed "gofeed/internal/domain/feed"
)

func encodeRawCursor(t *testing.T, payload string) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString([]byte(payload))
}

func encodeCursorFields(t *testing.T, fields map[string]any) string {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("构造游标载荷失败: %v", err)
	}
	return encodeRawCursor(t, string(data))
}

func validCursorFields() map[string]any {
	return map[string]any{
		"version":      1,
		"scene":        "timeline",
		"sort_version": 1,
		"published_at": "2026-05-04T03:02:01Z",
		"video_id":     899,
	}
}

// 测试目标：游标载荷的版本、场景与排序版本任一不符即拒绝
// 预期效果：三类版本字段错配都返回 ErrInvalidCursor
func TestDecodeTimelineCursorRejectsVersionMismatch(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"结构版本不符", map[string]any{"version": 2}},
		{"场景不符", map[string]any{"scene": "following"}},
		{"排序版本不符", map[string]any{"sort_version": 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := validCursorFields()
			for key, value := range tc.fields {
				fields[key] = value
			}
			if _, err := decodeTimelineCursor(encodeCursorFields(t, fields)); !errors.Is(err, domainfeed.ErrInvalidCursor) {
				t.Fatalf("got error=%v want=%v", err, domainfeed.ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：游标必须携带非零位置信息
// 预期效果：缺少发布时间、零发布时间与零视频 ID 都返回 ErrInvalidCursor
func TestDecodeTimelineCursorRejectsMissingPosition(t *testing.T) {
	cases := []struct {
		name   string
		fields map[string]any
	}{
		{"缺少发布时间", map[string]any{"published_at": nil}},
		{"发布时间为零值", map[string]any{"published_at": "0001-01-01T00:00:00Z"}},
		{"视频 ID 为零", map[string]any{"video_id": 0}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := validCursorFields()
			for key, value := range tc.fields {
				fields[key] = value
			}
			if _, err := decodeTimelineCursor(encodeCursorFields(t, fields)); !errors.Is(err, domainfeed.ErrInvalidCursor) {
				t.Fatalf("got error=%v want=%v", err, domainfeed.ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：游标拒绝未知字段与尾随内容
// 预期效果：多出字段或第二个 JSON 值都返回 ErrInvalidCursor
func TestDecodeTimelineCursorRejectsUnknownAndTrailingContent(t *testing.T) {
	fields := validCursorFields()
	fields["scene_id"] = 1
	if _, err := decodeTimelineCursor(encodeCursorFields(t, fields)); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("未知字段 got error=%v", err)
	}

	valid := encodeCursorFields(t, validCursorFields())
	for _, suffix := range []string{"{}", "null", "[]", "\n{\"version\":1}"} {
		payload := encodeRawCursor(t, decodeRawPayload(t, valid)+suffix)
		if _, err := decodeTimelineCursor(payload); !errors.Is(err, domainfeed.ErrInvalidCursor) {
			t.Fatalf("尾随内容 %q got error=%v", suffix, err)
		}
	}

	// 尾随空白不是第二个 JSON 值，允许被忽略
	withSpace := encodeRawCursor(t, decodeRawPayload(t, valid)+" ")
	if _, err := decodeTimelineCursor(withSpace); err != nil {
		t.Fatalf("尾随空白不应被拒绝 got error=%v", err)
	}
}

// 测试目标：游标编码必须严格可校验
// 预期效果：带填充、非规范编码、非法 JSON 与超长输入都返回 ErrInvalidCursor
func TestDecodeTimelineCursorRejectsMalformedEncoding(t *testing.T) {
	valid := encodeCursorFields(t, validCursorFields())
	oversized := strings.Repeat("A", maxCursorLength+1)

	cases := []struct {
		name   string
		cursor string
	}{
		{"带填充的 base64", valid + "=="},
		{"含非法字符", "!!!!"},
		{"非 JSON 载荷", encodeRawCursor(t, "not-json")},
		{"截断的 JSON", encodeRawCursor(t, `{"version":1,"scene":"timeline"`)},
		{"超过长度上限", oversized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeTimelineCursor(tc.cursor); !errors.Is(err, domainfeed.ErrInvalidCursor) {
				t.Fatalf("got error=%v want=%v", err, domainfeed.ErrInvalidCursor)
			}
		})
	}
}

// 测试目标：游标长度上限按 1024 字节判定
// 预期效果：恰好等于上限时不因长度被拒绝，超过上限直接拒绝
func TestDecodeTimelineCursorLengthBoundary(t *testing.T) {
	atLimit := strings.Repeat("A", maxCursorLength)
	if _, err := decodeTimelineCursor(atLimit); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("恰好等于上限应因载荷非法被拒绝 got error=%v", err)
	}
	if len(atLimit) != maxCursorLength {
		t.Fatalf("长度构造错误 got=%d", len(atLimit))
	}

	overLimit := strings.Repeat("A", maxCursorLength+1)
	if _, err := decodeTimelineCursor(overLimit); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("超过上限 got error=%v", err)
	}
}

// 测试目标：等价时区的发布时间解析为同一时刻
// 预期效果：带偏移的发布时间与 UTC 表示得到相同位置
func TestDecodeTimelineCursorNormalizesTimezone(t *testing.T) {
	fields := validCursorFields()
	fields["published_at"] = "2026-05-04T11:02:01+08:00"
	decoded, err := decodeTimelineCursor(encodeCursorFields(t, fields))
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if !decoded.PublishedAt.Equal(originTime) {
		t.Fatalf("时间归一失败 got=%s want=%s", decoded.PublishedAt, originTime)
	}
	if decoded.PublishedAt.UTC() != originTime.UTC() {
		t.Fatalf("UTC 时刻不一致 got=%s want=%s", decoded.PublishedAt.UTC(), originTime.UTC())
	}
}

// 测试目标：旧 /api/video 游标字段结构不能在新时间线解码
// 预期效果：旧字段名被当作未知字段拒绝
func TestDecodeTimelineCursorRejectsLegacyVideoPayload(t *testing.T) {
	legacy := encodeCursorFields(t, map[string]any{
		"v": 1, "k": "published", "a": 0, "p": "2026-05-04T03:02:01Z", "i": 899,
	})
	if _, err := decodeTimelineCursor(legacy); !errors.Is(err, domainfeed.ErrInvalidCursor) {
		t.Fatalf("旧游标 got error=%v want=%v", err, domainfeed.ErrInvalidCursor)
	}
}

// 测试目标：编码的时间统一为 UTC 便于跨时区比较
// 预期效果：本地时区位置编码后载荷渲染为 UTC 且解码得到同一时刻
func TestEncodeTimelineCursorKeepsInstant(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*3600)
	position := &domainfeed.TimelineCursor{
		PublishedAt: originTime.In(location),
		VideoID:     42,
	}
	encoded, err := encodeTimelineCursor(position)
	if err != nil {
		t.Fatalf("编码失败: %v", err)
	}
	wantPayload := fmt.Sprintf(`"published_at":%q`, originTime.UTC().Format(time.RFC3339Nano))
	if payload := decodeRawPayload(t, encoded); !strings.Contains(payload, wantPayload) {
		t.Fatalf("编码载荷未按 UTC 渲染 got=%s want 含 %s", payload, wantPayload)
	}
	decoded, err := decodeTimelineCursor(encoded)
	if err != nil {
		t.Fatalf("解码失败: %v", err)
	}
	if decoded.VideoID != 42 || !decoded.PublishedAt.Equal(originTime) {
		t.Fatalf("got=%+v want PublishedAt=%s VideoID=42", decoded, originTime)
	}
}

func decodeRawPayload(t *testing.T, encoded string) string {
	t.Helper()
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("还原游标载荷失败: %v", err)
	}
	return string(data)
}
