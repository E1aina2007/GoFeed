package interfaceshttprelation

import (
	"time"

	applicationrelation "gofeed/internal/application/relation"
	domainrelation "gofeed/internal/domain/relation"
)

type followStateResponse struct {
	Following     bool  `json:"following"`
	FollowerCount int64 `json:"follower_count"`
}

func followStateFromDomain(state domainrelation.FollowState) followStateResponse {
	return followStateResponse{Following: state.Following, FollowerCount: state.FollowerCount}
}

type publicUser struct {
	ID        uint   `json:"id"`
	Username  string `json:"username"`
	AvatarURL string `json:"avatar_url,omitempty"`
	Bio       string `json:"bio,omitempty"`
}

type followListItem struct {
	User       publicUser `json:"user"`
	FollowedAt time.Time  `json:"followed_at"`
}

type followListResponse struct {
	Items      []followListItem `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

func followListFromApplication(result applicationrelation.FollowListResult) followListResponse {
	response := followListResponse{Items: make([]followListItem, 0, len(result.Items)), NextCursor: result.NextCursor}
	for _, item := range result.Items {
		response.Items = append(response.Items, followListItem{
			User: publicUser{
				ID: item.User.ID, Username: item.User.Username,
				AvatarURL: item.User.AvatarURL, Bio: item.User.Bio,
			},
			FollowedAt: item.FollowedAt,
		})
	}
	return response
}
