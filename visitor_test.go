package gorgaws

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"

	"github.com/toddwbucy/gorg-aws/internal"
)

// ── mock implementations ──────────────────────────────────────────────────

type mockOrgLister struct {
	accounts []orgtypes.Account
	err      error
}

func (m *mockOrgLister) ListAccounts(_ context.Context, _ *organizations.ListAccountsInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &organizations.ListAccountsOutput{Accounts: m.accounts}, nil
}

func (m *mockOrgLister) ListAccountsForParent(_ context.Context, _ *organizations.ListAccountsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &organizations.ListAccountsForParentOutput{Accounts: m.accounts}, nil
}

func (m *mockOrgLister) ListOrganizationalUnitsForParent(_ context.Context, _ *organizations.ListOrganizationalUnitsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	// No child OUs by default — tests that need nested OUs use the internal stub directly.
	return &organizations.ListOrganizationalUnitsForParentOutput{}, nil
}

// newTestVisitor wires a visitor with a mock org client. Region discovery uses
// the static AllowedRegions list — no EC2 mock needed.
func newTestVisitor(
	accounts []orgtypes.Account,
	assumeFn func(context.Context, aws.Config, string, string, string) (aws.Config, error),
	opts ...Option,
) *OrgVisitor {
	v := New(aws.Config{}, opts...)
	v.newOrgClient = func(_ aws.Config) internal.OrgLister {
		return &mockOrgLister{accounts: accounts}
	}
	if assumeFn != nil {
		v.assumeRole = assumeFn
	} else {
		v.assumeRole = func(_ context.Context, base aws.Config, _, _, _ string) (aws.Config, error) {
			return base, nil
		}
	}
	return v
}

func activeAccount(id string) orgtypes.Account {
	return orgtypes.Account{Id: &id, State: orgtypes.AccountStateActive}
}

// ── EnvFromRegion ─────────────────────────────────────────────────────────

func TestEnvFromRegion(t *testing.T) {
	tests := []struct {
		region  string
		wantEnv string
		wantErr bool
	}{
		{"us-east-1", "com", false},
		{"us-east-2", "com", false},
		{"us-west-1", "com", false},
		{"us-west-2", "com", false},
		{"us-gov-east-1", "gov", false},
		{"us-gov-west-1", "gov", false},
		{"eu-west-1", "", true},
		{"ap-southeast-1", "", true},
		{"us-east-3", "", true}, // not in the allowed set
		{"", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.region, func(t *testing.T) {
			got, err := EnvFromRegion(tt.region)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EnvFromRegion(%q) err=%v, wantErr=%v", tt.region, err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidEnv) {
					t.Errorf("expected ErrInvalidEnv, got %v", err)
				}
				return
			}
			if got != tt.wantEnv {
				t.Errorf("got %q, want %q", got, tt.wantEnv)
			}
		})
	}
}

// ── AllowedRegions ────────────────────────────────────────────────────────

func TestAllowedRegions(t *testing.T) {
	com := AllowedRegions(false)
	if len(com) != 4 {
		t.Errorf("COM: got %d regions, want 4: %v", len(com), com)
	}
	gov := AllowedRegions(true)
	if len(gov) != 2 {
		t.Errorf("GOV: got %d regions, want 2: %v", len(gov), gov)
	}
	for _, r := range gov {
		env, _ := EnvFromRegion(r)
		if env != "gov" {
			t.Errorf("GOV region %q does not round-trip through EnvFromRegion", r)
		}
	}
	for _, r := range com {
		env, _ := EnvFromRegion(r)
		if env != "com" {
			t.Errorf("COM region %q does not round-trip through EnvFromRegion", r)
		}
	}
}

// ── envConfig ─────────────────────────────────────────────────────────────

func TestEnvConfig(t *testing.T) {
	tests := []struct {
		env            string
		wantRegion     string
		wantIncludeGov bool
		wantErr        bool
	}{
		{"com", "us-east-1", false, false},
		{"gov", "us-gov-west-1", true, false},
		{"COM", "", false, true},
		{"", "", false, true},
		{"azure", "", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.env, func(t *testing.T) {
			region, includeGov, err := envConfig(tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("envConfig(%q) err=%v, wantErr=%v", tt.env, err, tt.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidEnv) {
					t.Errorf("expected ErrInvalidEnv, got %v", err)
				}
				return
			}
			if region != tt.wantRegion {
				t.Errorf("region=%q, want %q", region, tt.wantRegion)
			}
			if includeGov != tt.wantIncludeGov {
				t.Errorf("includeGov=%v, want %v", includeGov, tt.wantIncludeGov)
			}
		})
	}
}

// ── VisitOrganization ─────────────────────────────────────────────────────

func TestVisitOrganization_CallsVisitors(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
	}

	var mu sync.Mutex
	var seenAccounts []string
	var seenRegions []string

	v := newTestVisitor(accounts, nil)

	results, err := v.VisitOrganization(context.Background(), "com",
		func(_ context.Context, _ aws.Config, accountID string) (any, error) {
			mu.Lock()
			seenAccounts = append(seenAccounts, accountID)
			mu.Unlock()
			return "account-result", nil
		},
		func(_ context.Context, _ aws.Config, accountID, region string) (any, error) {
			mu.Lock()
			seenRegions = append(seenRegions, accountID+"/"+region)
			mu.Unlock()
			return "region-result", nil
		},
		"",
	)
	if err != nil {
		t.Fatalf("VisitOrganization returned error: %v", err)
	}
	if results.TotalErrors != 0 {
		t.Errorf("TotalErrors=%d, want 0", results.TotalErrors)
	}
	if results.SuccessRate() != 1.0 {
		t.Errorf("SuccessRate=%f, want 1.0", results.SuccessRate())
	}
	if len(seenAccounts) != 2 {
		t.Errorf("AccountVisitor called %d times, want 2", len(seenAccounts))
	}
	// 2 accounts × 4 CONUS regions = 8 region visits.
	if len(seenRegions) != 8 {
		t.Errorf("RegionVisitor called %d times, want 8", len(seenRegions))
	}
	for _, id := range []string{"111111111111", "222222222222"} {
		ar, ok := results.Accounts[id]
		if !ok {
			t.Errorf("missing result for account %s", id)
			continue
		}
		if ar.Result != "account-result" {
			t.Errorf("account %s: Result=%v, want account-result", id, ar.Result)
		}
		for _, r := range AllowedRegions(false) {
			rr, ok := ar.Regions[r]
			if !ok {
				t.Errorf("account %s missing region %s", id, r)
				continue
			}
			if rr.Result != "region-result" {
				t.Errorf("account %s region %s: Result=%v, want region-result", id, r, rr.Result)
			}
		}
	}
}

func TestVisitOrganization_AssumeRoleFailure(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
	}

	v := newTestVisitor(accounts,
		func(_ context.Context, base aws.Config, accountID, _, _ string) (aws.Config, error) {
			if accountID == "111111111111" {
				return aws.Config{}, errors.New("access denied")
			}
			return base, nil
		},
	)

	results, err := v.VisitOrganization(context.Background(), "com", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected fatal error: %v", err)
	}
	if results.TotalErrors != 1 {
		t.Errorf("TotalErrors=%d, want 1", results.TotalErrors)
	}
	if !errors.Is(results.Accounts["111111111111"].Err, ErrAssumeRole) {
		t.Errorf("expected ErrAssumeRole for failed account, got %v", results.Accounts["111111111111"].Err)
	}
	if results.Accounts["222222222222"].Err != nil {
		t.Errorf("expected no error for successful account, got %v", results.Accounts["222222222222"].Err)
	}
}

func TestVisitOrganization_InvalidEnv(t *testing.T) {
	v := newTestVisitor(nil, nil)
	_, err := v.VisitOrganization(context.Background(), "invalid", nil, nil, "")
	if !errors.Is(err, ErrInvalidEnv) {
		t.Errorf("expected ErrInvalidEnv, got %v", err)
	}
}

func TestVisitOrganization_AccountFilter(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
		activeAccount("333333333333"),
	}

	var mu sync.Mutex
	visited := make(map[string]bool)

	v := newTestVisitor(accounts, nil,
		WithAccountFilter(func(id string) bool {
			return id == "222222222222"
		}),
	)

	results, err := v.VisitOrganization(context.Background(), "com",
		func(_ context.Context, _ aws.Config, accountID string) (any, error) {
			mu.Lock()
			visited[accountID] = true
			mu.Unlock()
			return nil, nil
		},
		nil, "",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if visited["222222222222"] {
		t.Error("filtered account was still visited")
	}
	if !visited["111111111111"] || !visited["333333333333"] {
		t.Error("non-filtered accounts should have been visited")
	}
	if _, ok := results.Accounts["222222222222"]; ok {
		t.Error("filtered account should not appear in results")
	}
}

func TestVisitOrganization_NilVisitors(t *testing.T) {
	accounts := []orgtypes.Account{activeAccount("111111111111")}
	v := newTestVisitor(accounts, nil)

	results, err := v.VisitOrganization(context.Background(), "com", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results.TotalErrors != 0 {
		t.Errorf("TotalErrors=%d, want 0", results.TotalErrors)
	}
}

// TestVisitOrganization_WithRoleName verifies that the role name option is
// passed through to the assumeRole function.
func TestVisitOrganization_WithRoleName(t *testing.T) {
	accounts := []orgtypes.Account{activeAccount("111111111111")}
	var capturedRole string

	v := newTestVisitor(accounts,
		func(_ context.Context, base aws.Config, _, _, roleName string) (aws.Config, error) {
			capturedRole = roleName
			return base, nil
		},
		WithRoleName("MyCustomRole"),
	)

	_, err := v.VisitOrganization(context.Background(), "com", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedRole != "MyCustomRole" {
		t.Errorf("assumeRole called with role=%q, want MyCustomRole", capturedRole)
	}
}

// TestVisitOrganization_WithParentID verifies that a non-empty parentID is
// respected: accounts are drawn from ListAccountsForParent, not ListAccounts.
func TestVisitOrganization_WithParentID(t *testing.T) {
	accounts := []orgtypes.Account{activeAccount("555555555555")}

	var mu sync.Mutex
	var visited []string

	v := newTestVisitor(accounts, nil)

	_, err := v.VisitOrganization(context.Background(), "com",
		func(_ context.Context, _ aws.Config, accountID string) (any, error) {
			mu.Lock()
			visited = append(visited, accountID)
			mu.Unlock()
			return nil, nil
		},
		nil,
		"ou-xxxx-xxxxxxxx",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(visited) != 1 || visited[0] != "555555555555" {
		t.Errorf("visited %v, want [555555555555]", visited)
	}
}

// TestVisitOrganization_Race exercises concurrent account+region visits under
// the race detector. Run with: go test -race ./...
func TestVisitOrganization_Race(t *testing.T) {
	const numAccounts = 20
	accounts := make([]orgtypes.Account, numAccounts)
	for i := range accounts {
		id := fmt.Sprintf("%012d", i)
		accounts[i] = activeAccount(id)
	}

	v := newTestVisitor(accounts, nil, WithConcurrency(5))

	results, err := v.VisitOrganization(context.Background(), "com",
		func(_ context.Context, _ aws.Config, _ string) (any, error) {
			return "ok", nil
		},
		func(_ context.Context, _ aws.Config, _, _ string) (any, error) {
			return "ok", nil
		},
		"",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results.Accounts) != numAccounts {
		t.Errorf("got %d accounts, want %d", len(results.Accounts), numAccounts)
	}
}

// ── DryRun ────────────────────────────────────────────────────────────────

func TestDryRun_ReturnsAccountsAndRegions(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
	}

	v := newTestVisitor(accounts, nil)

	gotAccounts, gotRegions, err := v.DryRun(context.Background(), "com", "")
	if err != nil {
		t.Fatalf("DryRun error: %v", err)
	}
	if len(gotAccounts) != 2 {
		t.Errorf("got %d accounts, want 2", len(gotAccounts))
	}
	// com env → 4 CONUS regions.
	if len(gotRegions) != 4 {
		t.Errorf("got %d regions, want 4", len(gotRegions))
	}
}

func TestDryRun_GOVReturnsGovRegions(t *testing.T) {
	accounts := []orgtypes.Account{activeAccount("111111111111")}
	v := newTestVisitor(accounts, nil)

	_, gotRegions, err := v.DryRun(context.Background(), "gov", "")
	if err != nil {
		t.Fatalf("DryRun error: %v", err)
	}
	if len(gotRegions) != 2 {
		t.Errorf("got %d GOV regions, want 2: %v", len(gotRegions), gotRegions)
	}
	for _, r := range gotRegions {
		env, _ := EnvFromRegion(r)
		if env != "gov" {
			t.Errorf("DryRun(gov) returned non-gov region %q", r)
		}
	}
}

func TestDryRun_InvalidEnv(t *testing.T) {
	v := newTestVisitor(nil, nil)
	_, _, err := v.DryRun(context.Background(), "bad", "")
	if !errors.Is(err, ErrInvalidEnv) {
		t.Errorf("expected ErrInvalidEnv, got %v", err)
	}
}

func TestDryRun_AppliesFilter(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
	}
	v := newTestVisitor(accounts, nil,
		WithAccountFilter(func(id string) bool { return id == "222222222222" }),
	)

	gotAccounts, _, err := v.DryRun(context.Background(), "com", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotAccounts) != 1 || gotAccounts[0] != "111111111111" {
		t.Errorf("DryRun returned %v, want [111111111111]", gotAccounts)
	}
}

// ── VisitResults helpers ──────────────────────────────────────────────────

func TestVisitResults_SuccessRate(t *testing.T) {
	tests := []struct {
		name     string
		accounts map[string]*AccountResult
		want     float64
	}{
		{
			name:     "empty",
			accounts: map[string]*AccountResult{},
			want:     0,
		},
		{
			name: "all success",
			accounts: map[string]*AccountResult{
				"a": {Err: nil},
				"b": {Err: nil},
			},
			want: 1.0,
		},
		{
			name: "half failure",
			accounts: map[string]*AccountResult{
				"a": {Err: nil},
				"b": {Err: errors.New("boom")},
			},
			want: 0.5,
		},
		{
			name: "all failure",
			accounts: map[string]*AccountResult{
				"a": {Err: errors.New("x")},
			},
			want: 0,
		},
		{
			name: "region-only failure",
			accounts: map[string]*AccountResult{
				"a": {
					Err:     nil,
					Regions: map[string]*RegionResult{"us-east-1": {Err: errors.New("throttled")}},
				},
				"b": {Err: nil},
			},
			want: 0.5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &VisitResults{Accounts: tt.accounts}
			if got := r.SuccessRate(); got != tt.want {
				t.Errorf("SuccessRate()=%f, want %f", got, tt.want)
			}
		})
	}
}

func TestVisitResults_SuccessfulAndFailedAccounts(t *testing.T) {
	r := &VisitResults{
		Accounts: map[string]*AccountResult{
			"a": {Err: nil},
			"b": {Err: errors.New("boom")},
			"c": {Err: nil},
			"d": {
				Err:     nil,
				Regions: map[string]*RegionResult{"us-east-1": {Err: errors.New("region boom")}},
			},
		},
	}
	if len(r.SuccessfulAccounts()) != 2 {
		t.Errorf("SuccessfulAccounts=%d, want 2 (a and c)", len(r.SuccessfulAccounts()))
	}
	if len(r.FailedAccounts()) != 2 {
		t.Errorf("FailedAccounts=%d, want 2 (b and d)", len(r.FailedAccounts()))
	}
}

// ── Benchmarks ────────────────────────────────────────────────────────────

// discardLogger returns a logger that discards all output — used in benchmarks
// to prevent slog from dominating the measured time.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// BenchmarkVisitOrganization measures throughput of the concurrent visitor
// against a mock org. Adjust numAccounts to model org size.
//
// Run: go test -bench=. -benchtime=5s ./...
func BenchmarkVisitOrganization(b *testing.B) {
	const numAccounts = 50
	accounts := make([]orgtypes.Account, numAccounts)
	for i := range accounts {
		id := fmt.Sprintf("%012d", i)
		accounts[i] = activeAccount(id)
	}

	v := newTestVisitor(accounts, nil, WithConcurrency(10), WithLogger(discardLogger()))
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := v.VisitOrganization(ctx, "com",
			func(_ context.Context, _ aws.Config, _ string) (any, error) { return nil, nil },
			func(_ context.Context, _ aws.Config, _, _ string) (any, error) { return nil, nil },
			"",
		)
		if err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}

// BenchmarkVisitOrganization_Sequential runs the same workload with
// concurrency=1, giving a baseline to compare against the parallel default.
func BenchmarkVisitOrganization_Sequential(b *testing.B) {
	const numAccounts = 50
	accounts := make([]orgtypes.Account, numAccounts)
	for i := range accounts {
		id := fmt.Sprintf("%012d", i)
		accounts[i] = activeAccount(id)
	}

	v := newTestVisitor(accounts, nil, WithConcurrency(1), WithLogger(discardLogger()))
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := v.VisitOrganization(ctx, "com",
			func(_ context.Context, _ aws.Config, _ string) (any, error) { return nil, nil },
			func(_ context.Context, _ aws.Config, _, _ string) (any, error) { return nil, nil },
			"",
		)
		if err != nil {
			b.Fatalf("unexpected error: %v", err)
		}
	}
}
