package gorgaws

import "log/slog"

// Option is a functional option for configuring an OrgVisitor.
type Option func(*OrgVisitor)

// WithConcurrency sets the maximum number of accounts processed concurrently.
// Defaults to 10. Values <= 0 are ignored.
func WithConcurrency(n int) Option {
	return func(v *OrgVisitor) {
		if n > 0 {
			v.concurrency = n
		}
	}
}

// WithRoleName overrides the IAM role name assumed in each target account.
// Defaults to "OrganizationAccountAccessRole". Empty strings are ignored.
func WithRoleName(name string) Option {
	return func(v *OrgVisitor) {
		if name != "" {
			v.roleName = name
		}
	}
}

// WithLogger sets the structured logger used for visit progress and errors.
// Defaults to slog.Default(). Nil values are ignored.
func WithLogger(l *slog.Logger) Option {
	return func(v *OrgVisitor) {
		if l != nil {
			v.logger = l
		}
	}
}

// WithAccountFilter registers a predicate that is called with each account ID
// before it is visited. Accounts for which fn returns true are skipped.
// This allows excluding management accounts, sandbox accounts, etc.
func WithAccountFilter(fn func(accountID string) bool) Option {
	return func(v *OrgVisitor) {
		v.filter = fn
	}
}
