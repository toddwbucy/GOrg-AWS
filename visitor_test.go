package gorgaws

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
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

type mockRegionDescriber struct {
	regions []string
	err     error
}

func (m *mockRegionDescriber) DescribeRegions(_ context.Context, _ *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := &ec2.DescribeRegionsOutput{}
	for _, r := range m.regions {
		name := r
		out.Regions = append(out.Regions, ec2types.Region{RegionName: &name})
	}
	return out, nil
}

// newTestVisitor wires a visitor with mock clients. If assumeFn is nil, a
// no-op assume that returns the base config unchanged is used.
func newTestVisitor(
	accounts []orgtypes.Account,
	regions []string,
	assumeFn func(context.Context, aws.Config, string, string, string) (aws.Config, error),
	opts ...Option,
) *OrgVisitor {
	v := New(aws.Config{}, opts...)
	orgMock := &mockOrgLister{accounts: accounts}
	ec2Mock := &mockRegionDescriber{regions: regions}
	v.newOrgClient = func(_ aws.Config) internal.OrgLister { return orgMock }
	v.newEC2Client = func(_ aws.Config) internal.RegionDescriber { return ec2Mock }
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
	regions := []string{"us-east-1", "us-west-2"}

	var mu sync.Mutex
	var seenAccounts []string
	var seenRegions []string

	v := newTestVisitor(accounts, regions, nil)

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
	// 2 accounts × 2 regions = 4 region visits.
	if len(seenRegions) != 4 {
		t.Errorf("RegionVisitor called %d times, want 4", len(seenRegions))
	}
	// Verify result storage.
	for _, id := range []string{"111111111111", "222222222222"} {
		ar, ok := results.Accounts[id]
		if !ok {
			t.Errorf("missing result for account %s", id)
			continue
		}
		if ar.Result != "account-result" {
			t.Errorf("account %s: Result=%v, want account-result", id, ar.Result)
		}
		for _, r := range regions {
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

	v := newTestVisitor(accounts, []string{"us-east-1"},
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
	v := newTestVisitor(nil, nil, nil)
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

	v := newTestVisitor(accounts, []string{"us-east-1"}, nil,
		WithAccountFilter(func(id string) bool {
			return id == "222222222222" // skip this account
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
	v := newTestVisitor(accounts, []string{"us-east-1"}, nil)

	// Both visitors nil — should complete without panic.
	results, err := v.VisitOrganization(context.Background(), "com", nil, nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results.TotalErrors != 0 {
		t.Errorf("TotalErrors=%d, want 0", results.TotalErrors)
	}
}

// ── DryRun ────────────────────────────────────────────────────────────────

func TestDryRun_ReturnsAccountsAndRegions(t *testing.T) {
	accounts := []orgtypes.Account{
		activeAccount("111111111111"),
		activeAccount("222222222222"),
	}
	regions := []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2"}

	v := newTestVisitor(accounts, regions, nil)

	gotAccounts, gotRegions, err := v.DryRun(context.Background(), "com", "")
	if err != nil {
		t.Fatalf("DryRun error: %v", err)
	}
	if len(gotAccounts) != 2 {
		t.Errorf("got %d accounts, want 2", len(gotAccounts))
	}
	if len(gotRegions) != 4 {
		t.Errorf("got %d regions, want 4", len(gotRegions))
	}
}

func TestDryRun_InvalidEnv(t *testing.T) {
	v := newTestVisitor(nil, nil, nil)
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
	v := newTestVisitor(accounts, []string{"us-east-1"}, nil,
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

func TestDryRun_AllFilteredSkipsRegionDiscovery(t *testing.T) {
	accounts := []orgtypes.Account{activeAccount("111111111111")}
	// ec2Mock errors — if GetUSRegions were called the test would fail.
	v := New(aws.Config{})
	orgMock := &mockOrgLister{accounts: accounts}
	ec2Mock := &mockRegionDescriber{err: errors.New("should not be called")}
	v.newOrgClient = func(_ aws.Config) internal.OrgLister { return orgMock }
	v.newEC2Client = func(_ aws.Config) internal.RegionDescriber { return ec2Mock }
	v.assumeRole = func(_ context.Context, base aws.Config, _, _, _ string) (aws.Config, error) { return base, nil }
	v.filter = func(_ string) bool { return true } // filter everything

	gotAccounts, gotRegions, err := v.DryRun(context.Background(), "com", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotAccounts) != 0 {
		t.Errorf("got %d accounts, want 0", len(gotAccounts))
	}
	if len(gotRegions) != 0 {
		t.Errorf("got %d regions, want 0 (region discovery should be skipped)", len(gotRegions))
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
			// account-level Err is nil but a region failed — should be counted as failed
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
