package video

import (
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PublicVideoScope 为以 Video 作为当前 Model 的查询附加公开视频数据边界
// GORM 的默认软删除作用域负责排除 deleted_at 非空的记录
func PublicVideoScope(db *gorm.DB) *gorm.DB {
	column := func(name string) clause.Column {
		return clause.Column{Table: clause.CurrentTable, Name: name}
	}

	return db.
		Where(clause.Eq{Column: column("status"), Value: VideoStatusPublished}).
		Where(clause.Neq{Column: column("published_at"), Value: nil}).
		Where(clause.Neq{Column: column("play_url"), Value: ""}).
		Where(clause.Neq{Column: column("play_file_name"), Value: ""}).
		Where(clause.Neq{Column: column("play_original_name"), Value: ""}).
		Where(clause.Neq{Column: column("cover_url"), Value: ""}).
		Where(clause.Neq{Column: column("cover_file_name"), Value: ""}).
		Where(clause.Neq{Column: column("cover_original_name"), Value: ""})
}
