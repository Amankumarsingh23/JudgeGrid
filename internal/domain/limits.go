package domain

import "fmt"

type LimitError struct {
	Code       string
	RetryAfter int
}

func (e *LimitError) Error() string { return fmt.Sprintf("request admission: %s", e.Code) }
