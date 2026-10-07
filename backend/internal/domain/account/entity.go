package domainaccount

type PublicAccount struct {
	ID        uint
	Username  string
	AvatarURL string
	Bio       string
}

type ListPosition struct {
	ID uint
}

type Profile struct {
	Account       PublicAccount
	VideoCount    int64
	TotalLikes    int64
	FollowerCount int64
	VloggerCount  int64
}

type ProfileMetrics struct {
	TotalLikes    int64
	FollowerCount int64
	VloggerCount  int64
}
