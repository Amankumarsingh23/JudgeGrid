package submission

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Amankumarsingh23/JudgeGrid/internal/domain"
)

// Repository is the persistence boundary; principals must come from trusted authentication wiring.
type Repository interface {
	Create(context.Context, string, string, domain.CreateRequest) (domain.Submission, bool, error)
	Get(context.Context, string, string) (domain.Submission, error)
	List(context.Context, string, int, *domain.Cursor) ([]domain.Submission, error)
	Problem(context.Context, string) (domain.Problem, error)
}
type Service struct {
	repo    Repository
	timeout time.Duration
}

func New(repo Repository, timeout time.Duration) *Service { return &Service{repo, timeout} }

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func ValidID(id string) bool       { return uuidPattern.MatchString(id) }
func principalValid(p string) bool { return strings.TrimSpace(p) != "" && len(p) <= 200 }

func (s *Service) Create(ctx context.Context, principal, key string, req domain.CreateRequest) (domain.Submission, bool, error) {
	if !principalValid(principal) {
		return domain.Submission{}, false, domain.ErrUnauthorized
	}
	if len(key) == 0 || len(key) > 200 {
		return domain.Submission{}, false, domain.ErrInvalid
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return domain.Submission{}, false, domain.ErrInvalid
		}
	}
	req = req.Normalize()
	if err := req.Validate(); err != nil {
		return domain.Submission{}, false, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.repo.Create(ctx, principal, key, req)
}
func (s *Service) Get(ctx context.Context, principal, id string) (domain.Submission, error) {
	if !principalValid(principal) {
		return domain.Submission{}, domain.ErrUnauthorized
	}
	if !ValidID(id) {
		return domain.Submission{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.repo.Get(ctx, principal, id)
}
func (s *Service) List(ctx context.Context, principal string, limit int, cursor string) (domain.Page, error) {
	if !principalValid(principal) {
		return domain.Page{}, domain.ErrUnauthorized
	}
	if limit < 1 || limit > 100 || len(cursor) > 512 {
		return domain.Page{}, domain.ErrInvalid
	}
	var after *domain.Cursor
	if cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return domain.Page{}, domain.ErrInvalid
		}
		var c domain.Cursor
		if json.Unmarshal(data, &c) != nil || c.CreatedAt.IsZero() || !ValidID(c.ID) {
			return domain.Page{}, domain.ErrInvalid
		}
		after = &c
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	items, err := s.repo.List(ctx, principal, limit+1, after)
	if err != nil {
		return domain.Page{}, err
	}
	page := domain.Page{Items: items}
	if page.Items == nil {
		page.Items = []domain.Submission{}
	}
	if len(items) > limit {
		last := items[limit-1]
		data, _ := json.Marshal(domain.Cursor{CreatedAt: last.CreatedAt, ID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
		page.Items = items[:limit]
	}
	return page, nil
}
func (s *Service) Problem(ctx context.Context, slug string) (domain.Problem, error) {
	if len(slug) == 0 || len(slug) > 100 {
		return domain.Problem{}, domain.ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	return s.repo.Problem(ctx, slug)
}
