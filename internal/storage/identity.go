package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
	"github.com/Amankumarsingh23/JudgeGrid/internal/identity"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/pbkdf2"
	"regexp"
)

var localPrincipal = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

func tokenHash(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}
func randomSecret() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func (r *Repository) CreateLocalUser(ctx context.Context, principal, password string) error {
	if !localPrincipal.MatchString(principal) || len(password) < 12 || len(password) > 128 {
		return domain.ErrInvalid
	}
	salt := make([]byte, 16)
	if _, e := rand.Read(salt); e != nil {
		return e
	}
	hash := pbkdf2.Key([]byte(password), salt, 210000, 32, sha256.New)
	_, e := r.pool.Exec(ctx, `INSERT INTO local_users(principal,salt,password_hash) VALUES($1,$2,$3)`, principal, salt, hash)
	return e
}
func (r *Repository) Login(ctx context.Context, principal, password string) (string, identity.Session, error) {
	var result identity.Session
	if len(principal) > 64 || len(password) > 128 {
		return "", result, domain.ErrUnauthorized
	}
	if e := r.consumeBucket(ctx, "login:global", 20, 2); e != nil {
		return "", result, e
	}
	if e := r.consumeBucket(ctx, "login:"+tokenHash(principal), 5, .2); e != nil {
		return "", result, e
	}
	salt := make([]byte, 16)
	expected := make([]byte, 32)
	e := r.pool.QueryRow(ctx, `SELECT salt,password_hash FROM local_users WHERE principal=$1`, principal).Scan(&salt, &expected)
	found := e == nil
	if e != nil && !errors.Is(e, pgx.ErrNoRows) {
		return "", result, e
	}
	actual := pbkdf2.Key([]byte(password), salt, 210000, 32, sha256.New)
	if subtle.ConstantTimeCompare(actual, expected) != 1 || !found {
		return "", result, domain.ErrUnauthorized
	}
	token := randomSecret()
	csrf := randomSecret()
	result.Principal = principal
	result.CSRF = csrf
	e = r.pool.QueryRow(ctx, `INSERT INTO local_sessions(token_hash,principal,csrf_token,expires_at) VALUES($1,$2,$3,clock_timestamp()+interval '30 minutes') RETURNING expires_at`, tokenHash(token), principal, csrf).Scan(&result.Expires)
	if e != nil {
		return "", identity.Session{}, e
	}
	return token, result, nil
}
func (r *Repository) Session(ctx context.Context, token string) (result identity.Session, e error) {
	if len(token) != 64 {
		return result, domain.ErrUnauthorized
	}
	e = r.pool.QueryRow(ctx, `SELECT principal,csrf_token,expires_at FROM local_sessions WHERE token_hash=$1 AND expires_at>clock_timestamp()`, tokenHash(token)).Scan(&result.Principal, &result.CSRF, &result.Expires)
	if errors.Is(e, pgx.ErrNoRows) {
		return result, domain.ErrUnauthorized
	}
	return result, e
}
func (r *Repository) Logout(ctx context.Context, token string) error {
	_, e := r.pool.Exec(ctx, `DELETE FROM local_sessions WHERE token_hash=$1`, tokenHash(token))
	return e
}
