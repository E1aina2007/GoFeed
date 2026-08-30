package video

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// mediaFileHeadBytes 是文件头校验所需读取的最大字节数，覆盖全部 magic bytes 规则
const mediaFileHeadBytes = 16

// ValidatePublishedMedia 校验共享存储下处理中视频的播放与封面文件完整可用
// 复用上传时的路径结构、大小上限、扩展名与文件头校验规则，
// 供异步处理消费端将媒体缺陷判定为确定性拒绝
func ValidatePublishedMedia(root, playURL, coverURL string) error {
	if err := validateStoredMediaFile(root, MediaVideo, playURL); err != nil {
		return fmt.Errorf("视频媒体不可用: %w", err)
	}
	if err := validateStoredMediaFile(root, MediaCover, coverURL); err != nil {
		return fmt.Errorf("封面媒体不可用: %w", err)
	}
	return nil
}

// validateStoredMediaFile 校验单个已存储媒体文件的路径、大小与文件头
// 返回的错误仅描述媒体缺陷或不可访问，不含路径穿越等安全歧义
func validateStoredMediaFile(root string, kind MediaKind, publicURL string) error {
	storage := &LocalStorage{root: root}
	path, err := storage.pathForPublicURL(publicURL)
	if err != nil {
		return err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: 文件不存在", ErrInvalidMedia)
		}
		return err
	}
	if info.Size() == 0 || info.Size() > maxMediaSize(kind) {
		return fmt.Errorf("%w: 文件大小越界", ErrInvalidMedia)
	}

	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()

	head := make([]byte, mediaFileHeadBytes)
	n, err := file.Read(head)
	if err != nil && err != io.EOF {
		return err
	}
	// 文件名以存储名为准，扩展名与文件头必须一致，防止改后缀绕过
	if !validateMedia(kind, filepath.Base(path), head[:n]) {
		return fmt.Errorf("%w: 文件头与扩展名不一致", ErrInvalidMedia)
	}
	return nil
}
