package gorgaws

import "time"

// VisitResults holds the aggregated output of a VisitOrganization call.
type VisitResults struct {
	// Accounts maps each account ID to its result.
	Accounts map[string]*AccountResult

	// TimeElapsed is the wall-clock duration of the full visit.
	TimeElapsed time.Duration

	// TotalErrors is the count of accounts where Err != nil.
	TotalErrors int
}

// AccountResult holds the output of visiting a single account.
type AccountResult struct {
	AccountID string

	// Result is the value returned by AccountVisitor, if one was provided.
	Result any

	// Err is non-nil if the account could not be assumed into, or if
	// AccountVisitor returned an error.
	Err error

	// Regions maps each region name to its RegionResult.
	Regions map[string]*RegionResult
}

// RegionResult holds the output of visiting a single account+region pair.
type RegionResult struct {
	Region string

	// Result is the value returned by RegionVisitor.
	Result any

	// Err is non-nil if RegionVisitor returned an error.
	Err error
}

// SuccessfulAccounts returns account results where Err == nil.
func (r *VisitResults) SuccessfulAccounts() []*AccountResult {
	out := make([]*AccountResult, 0, len(r.Accounts))
	for _, a := range r.Accounts {
		if a.Err == nil {
			out = append(out, a)
		}
	}
	return out
}

// FailedAccounts returns account results where Err != nil.
func (r *VisitResults) FailedAccounts() []*AccountResult {
	out := make([]*AccountResult, 0)
	for _, a := range r.Accounts {
		if a.Err != nil {
			out = append(out, a)
		}
	}
	return out
}

// SuccessRate returns the fraction of accounts with Err == nil.
// Returns 0 if no accounts were visited.
func (r *VisitResults) SuccessRate() float64 {
	if len(r.Accounts) == 0 {
		return 0
	}
	return float64(len(r.SuccessfulAccounts())) / float64(len(r.Accounts))
}
