package domainrelation

type FollowState struct {
	Following     bool
	FollowerCount int64
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
