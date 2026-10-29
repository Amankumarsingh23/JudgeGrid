package identity

import (
	"context"
	"time"
)

type Session struct {
	Principal string    `json:"principal"`
	CSRF      string    `json:"csrf_token"`
	Expires   time.Time `json:"expires_at"`
}
type Store interface {
	Login(context.Context, string, string) (string, Session, error)
	Session(context.Context, string) (Session, error)
	Logout(context.Context, string) error
}
