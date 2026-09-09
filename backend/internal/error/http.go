package apierror

import (
	"github.com/gin-gonic/gin"
)

// Write 解析错误并按统一 {"error":"..."} 形状写入响应
// 成功状态码仍由具体 handler 显式写入，本函数只处理失败响应
func Write(c *gin.Context, err error, fallback string, rules ...Rule) {
	WriteJSON(c, Resolve(err, fallback, rules...))
}

// WriteJSON 按错误描述写入统一的 JSON 错误响应
func WriteJSON(c *gin.Context, descriptor Descriptor) {
	c.JSON(HTTPStatus(descriptor.Code), gin.H{"error": descriptor.PublicMessage})
}

// WriteUnauthorized 写入统一的认证失败响应，供受保护端点在缺少有效会话时拒绝请求
func WriteUnauthorized(c *gin.Context, message string) {
	WriteJSON(c, Descriptor{Code: CodeUnauthorized, PublicMessage: message})
}

// WriteCode 按显式公共类别与文案写入错误响应，供端点固定的解析或上传失败分支复用
func WriteCode(c *gin.Context, code Code, message string) {
	WriteJSON(c, Descriptor{Code: code, PublicMessage: message})
}
