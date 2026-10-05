package infrainteraction

import "time"

const eventStatusPending = "pending"

type EventModel struct {
	ID                     uint   `gorm:"primaryKey"`
	EventID                string `gorm:"type:char(36);not null;uniqueIndex:uq_interaction_outbox_event_id"`
	SchemaVersion          int    `gorm:"type:int unsigned;not null"`
	EventType              string `gorm:"type:varchar(64);not null"`
	VideoID                uint   `gorm:"not null;index:idx_interaction_outbox_video"`
	Kind                   string `gorm:"type:varchar(32);not null;uniqueIndex:uq_interaction_outbox_change,priority:1"`
	InteractionID          uint   `gorm:"not null;uniqueIndex:uq_interaction_outbox_change,priority:2"`
	Delta                  int    `gorm:"type:tinyint;not null"`
	OccurredAtMs           int64  `gorm:"not null"`
	InteractionCreatedAtMs int64  `gorm:"not null"`
	Status                 string `gorm:"type:varchar(16);not null;default:pending"`
	Attempt                int    `gorm:"not null;default:0"`
	NextAttemptAt          *time.Time
	LockedUntil            *time.Time
	LastAttemptAt          *time.Time
	LastError              string    `gorm:"type:varchar(255);not null;default:''"`
	CreatedAt              time.Time `gorm:"not null"`
	DispatchedAt           *time.Time
}

func (EventModel) TableName() string {
	return "interaction_outbox_events"
}
