package domainrelation

import "time"

type FollowState struct {
	Following     bool
	FollowerCount int64
}

type PublicUser struct {
	ID        uint
	Username  string
	AvatarURL string
	Bio       string
}

type FollowListItem struct {
	User       PublicUser
	FollowedAt time.Time
	RelationID uint
}

type FollowPosition struct {
	CreatedAt time.Time
	ID        uint
}

func ValidateFollowUsers(followerID, followeeID uint) error {
	if followerID == 0 || followeeID == 0 {
		return ErrInvalidUserID
	}
	if followerID == followeeID {
		return ErrSelfFollow
	}
	return nil
}
