package gorgaws

import "errors"

// Sentinel errors — use errors.Is(err, gorgaws.ErrAssumeRole) for type-safe matching.
var (
	// ErrAssumeRole is returned when STS AssumeRole fails for a target account.
	ErrAssumeRole = errors.New("gorgaws: failed to assume role in account")

	// ErrOrgAPI is returned when the AWS Organizations API returns an unexpected error.
	ErrOrgAPI = errors.New("gorgaws: organizations API error")

	// ErrInvalidEnv is returned when env is not "com" or "gov".
	ErrInvalidEnv = errors.New("gorgaws: environment must be 'com' or 'gov'")
)
