package domainaccount

import "strings"

type ProfileChanges struct {
	AvatarURL string
	Bio       string
}

func NormalizeNewUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return "", ErrNewUserNameRequired
	}
	if len(username) < 3 || len(username) > 32 {
		return "", ErrInvalidInput
	}
	return username, nil
}

func NormalizeProfileChanges(changes ProfileChanges) (ProfileChanges, error) {
	changes.Bio = strings.TrimSpace(changes.Bio)
	changes.AvatarURL = strings.TrimSpace(changes.AvatarURL)
	if changes.Bio == "" && changes.AvatarURL == "" {
		return ProfileChanges{}, ErrNothingToUpdate
	}
	return changes, nil
}

func NormalizeAvatarURL(avatarURL string) (string, error) {
	avatarURL = strings.TrimSpace(avatarURL)
	if avatarURL == "" {
		return "", ErrInvalidInput
	}
	return avatarURL, nil
}
