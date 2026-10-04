package router

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
)

// 测试目标：认证载荷通过内联字段、参数列表或 SHA1 资源写入 trace 时均完成脱敏
// 预期效果：登录、注册和刷新凭据不可从压缩包恢复，普通响应与事件结构仍可读取
func TestFollowingTraceRedactionRemovesAuthRequestResources(t *testing.T) {
	for _, endpoint := range []string{"login", "register", "refresh"} {
		for _, resourcesFirst := range []bool{false, true} {
			name := endpoint + "/network_first"
			if resourcesFirst {
				name = endpoint + "/resources_first"
			}
			t.Run(name, func(t *testing.T) {
				const requestSecret = "opaque-unit-request-credential-unmatched-by-jwt-and-password-patterns"
				const responseSecret = "opaque-unit-response-credential-unmatched-by-jwt-and-password-patterns"
				const headerSecret = "opaque-unit-header-credential"
				requestBody, err := json.Marshal(map[string]string{"refresh_token": requestSecret})
				if err != nil {
					t.Fatal(err)
				}
				network, err := json.Marshal(map[string]any{
					"type": "resource-snapshot",
					"snapshot": map[string]any{
						"request": map[string]any{
							"url":     "http://localhost/api/user/" + endpoint,
							"headers": []any{map[string]any{"name": "Authorization", "value": "Bearer " + headerSecret}},
							"postData": map[string]any{
								"mimeType": "application/json",
								"text":     string(requestBody),
								"_sha1":    "auth-request.json",
								"params":   []any{map[string]any{"name": "refresh_token", "value": requestSecret}},
							},
						},
						"response": map[string]any{
							"status":  200,
							"content": map[string]any{"_sha1": "auth-response.json"},
						},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				resources := []followingTraceEntry{
					{header: &zip.FileHeader{Name: "resources/auth-request.json", Method: zip.Deflate}, data: requestBody},
					{header: &zip.FileHeader{Name: "resources/auth-response.json", Method: zip.Deflate}, data: []byte(responseSecret)},
					{header: &zip.FileHeader{Name: "resources/feed.json", Method: zip.Deflate}, data: []byte(`{"items":[{"id":42}],"next_cursor":"public-cursor"}`)},
				}
				entries := []followingTraceEntry{{header: &zip.FileHeader{Name: "0-trace.network", Method: zip.Deflate}, data: append(network, '\n')}}
				if resourcesFirst {
					entries = append(resources, entries...)
				} else {
					entries = append(entries, resources...)
				}
				tracePath := filepath.Join(t.TempDir(), "trace.zip")
				if err := writeFollowingZip(tracePath, entries); err != nil {
					t.Fatal(err)
				}
				if err := sanitizeFollowingTraceZip(tracePath); err != nil {
					t.Fatal(err)
				}
				reader, err := zip.OpenReader(tracePath)
				if err != nil {
					t.Fatal(err)
				}
				defer reader.Close()
				if len(reader.File) != len(entries) {
					t.Fatalf("脱敏前后条目数变化 got=%d want=%d", len(reader.File), len(entries))
				}
				for _, entry := range reader.File {
					opened, err := entry.Open()
					if err != nil {
						t.Fatal(err)
					}
					data, err := io.ReadAll(opened)
					opened.Close()
					if err != nil {
						t.Fatal(err)
					}
					for _, secret := range []string{requestSecret, responseSecret, headerSecret} {
						if bytes.Contains(data, []byte(secret)) {
							t.Fatalf("认证凭据仍存在于 trace 条目 %s", entry.Name)
						}
					}
					if !json.Valid(bytes.TrimSpace(data)) {
						t.Fatalf("脱敏后的条目不是有效 JSON: %s", entry.Name)
					}
					if entry.Name == "resources/feed.json" && !bytes.Equal(data, resources[2].data) {
						t.Fatal("普通 Feed 响应被脱敏过程修改")
					}
				}
			})
		}
	}
}
