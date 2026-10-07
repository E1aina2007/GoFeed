package interfaceshttprelation

import domainrelation "gofeed/internal/domain/relation"

type followStateResponse struct {
	Following     bool  `json:"following"`
	FollowerCount int64 `json:"follower_count"`
}

func followStateFromDomain(state domainrelation.FollowState) followStateResponse {
	return followStateResponse{Following: state.Following, FollowerCount: state.FollowerCount}
}
