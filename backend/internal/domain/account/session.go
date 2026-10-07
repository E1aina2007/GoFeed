package domainaccount

import "time"

type Credentials struct {
	Account      PublicAccount
	PasswordHash string
}

type Session struct {
	ID        string
	UserID    uint
	ExpiresAt time.Time
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
}

type SessionResult struct {
	Tokens  TokenPair
	Account PublicAccount
}
