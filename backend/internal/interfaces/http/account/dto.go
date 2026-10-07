package interfaceshttpaccount

import (
	applicationaccount "gofeed/internal/application/account"
	domainaccount "gofeed/internal/domain/account"
)

type registrationRequest struct {
	Username string `json:"username" binding:"required,min=3,max=32"`
	Password string `json:"password" binding:"required,min=8,max=72"`
}

type publicAccount struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

func publicAccountFromDomain(account domainaccount.PublicAccount) publicAccount {
	return publicAccount{ID: account.ID, Username: account.Username, AvatarURL: account.AvatarURL, Bio: account.Bio}
}

type userListResponse struct {
	Users      []publicAccount `json:"users"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func userListFromApplication(result applicationaccount.UserListResult) userListResponse {
	response := userListResponse{Users: make([]publicAccount, 0, len(result.Users)), NextCursor: result.NextCursor}
	for _, account := range result.Users {
		response.Users = append(response.Users, publicAccountFromDomain(account))
	}
	return response
}

type profileResponse struct {
	Account       publicAccount `json:"account"`
	VideoCount    int64         `json:"video_count"`
	TotalLikes    int64         `json:"total_likes"`
	FollowerCount int64         `json:"follower_count"`
	VloggerCount  int64         `json:"vlogger_count"`
}

func profileFromDomain(profile domainaccount.Profile) profileResponse {
	return profileResponse{
		Account: publicAccountFromDomain(profile.Account), VideoCount: profile.VideoCount,
		TotalLikes: profile.TotalLikes, FollowerCount: profile.FollowerCount, VloggerCount: profile.VloggerCount,
	}
}
