// Package gorgaws provides the AWS Organization Visitor pattern in Go.
//
// It is a direct equivalent of the internal Python org_visitor library:
// callers supply visitor functions and receive pre-assumed aws.Config values —
// they never touch role ARNs, STS calls, or credential structs.
//
// Unlike the sequential Python original, VisitOrganization processes accounts
// concurrently (default 10 at a time) and visits regions within each account
// in parallel, reducing wall-clock time from O(accounts × regions) to roughly
// O(slowest_account).
package gorgaws

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"

	"github.com/toddwbucy/gorg-aws/internal"
)

// AccountVisitor is called once per account with a pre-assumed aws.Config.
// cfg is loaded with assumed-role credentials for accountID — callers never
// touch auth directly.
//
// Equivalent of the Python original:
//
//	account_visitor(session, account_id)
type AccountVisitor func(ctx context.Context, cfg aws.Config, accountID string) (any, error)

// RegionVisitor is called once per account+region with a pre-assumed aws.Config.
// cfg.Region is set to the target region.
//
// Equivalent of the Python original:
//
//	region_visitor(session, region, account_id)
type RegionVisitor func(ctx context.Context, cfg aws.Config, accountID, region string) (any, error)

// OrgVisitor walks an AWS organization, assumes a role in each account, and
// invokes visitor functions with pre-assumed credentials.
// Create one with New(); reuse across multiple calls.
type OrgVisitor struct {
	baseCfg     aws.Config
	roleName    string
	concurrency int
	logger      *slog.Logger
	filter      func(string) bool

	// Injectable factories — real SDK clients by default, mocks in tests.
	newOrgClient func(aws.Config) internal.OrgLister
	assumeRole   func(context.Context, aws.Config, string, string, string) (aws.Config, error)
}

// New creates an OrgVisitor using baseCfg as the base credential source.
// baseCfg should be loaded with management-account credentials.
//
//	cfg, _ := config.LoadDefaultConfig(ctx,
//	    config.WithCredentialsProvider(
//	        credentials.NewStaticCredentialsProvider(key, secret, token),
//	    ),
//	)
//	v := gorgaws.New(cfg, gorgaws.WithConcurrency(20))
func New(baseCfg aws.Config, opts ...Option) *OrgVisitor {
	v := &OrgVisitor{
		baseCfg:     baseCfg,
		roleName:    "OrganizationAccountAccessRole",
		concurrency: 10,
		logger:      slog.Default(),
		newOrgClient: func(cfg aws.Config) internal.OrgLister {
			return organizations.NewFromConfig(cfg)
		},
		assumeRole: internal.AssumedConfig,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// EnvFromRegion returns "gov" for GovCloud regions and "com" for CONUS
// commercial regions. Returns ErrInvalidEnv if region is not in the allowed set.
//
// This is the preferred way to derive env for VisitOrganization when your
// config stores a region rather than an explicit "com"/"gov" label:
//
//	env, err := gorgaws.EnvFromRegion(cfg.HomeRegion)
//	results, err := v.VisitOrganization(ctx, env, onAccount, onRegion, "")
func EnvFromRegion(region string) (string, error) {
	switch region {
	case "us-gov-east-1", "us-gov-west-1":
		return "gov", nil
	case "us-east-1", "us-east-2", "us-west-1", "us-west-2":
		return "com", nil
	default:
		return "", fmt.Errorf("%w: region %q is not in the allowed set", ErrInvalidEnv, region)
	}
}

// AllowedRegions returns the fixed set of regions that will be visited.
// includeGov=false → ["us-east-1", "us-east-2", "us-west-1", "us-west-2"]
// includeGov=true  → ["us-gov-east-1", "us-gov-west-1"]
func AllowedRegions(includeGov bool) []string {
	return internal.AllowedRegions(includeGov)
}

// VisitOrganization walks every account in the organization (or under parentID)
// and calls onAccount and onRegion with pre-assumed credentials for each.
//
// env must be "com" or "gov" — use EnvFromRegion to derive it from a region name.
// parentID may be "" to walk the full organization.
// Either onAccount or onRegion may be nil to skip that visitor type.
//
// Errors from individual accounts are recorded in VisitResults.Accounts[id].Err
// and do not abort the walk. A non-nil returned error indicates a fatal failure
// (invalid env, or Organizations API unreachable).
func (v *OrgVisitor) VisitOrganization(
	ctx context.Context,
	env string,
	onAccount AccountVisitor,
	onRegion RegionVisitor,
	parentID string,
) (VisitResults, error) {
	start := time.Now()
	results := VisitResults{
		Accounts: make(map[string]*AccountResult),
	}

	homeRegion, includeGov, err := envConfig(env)
	if err != nil {
		return results, err
	}

	orgCfg := v.baseCfg.Copy()
	orgCfg.Region = homeRegion

	accountIDs, err := internal.ListAccounts(ctx, v.newOrgClient(orgCfg), parentID)
	if err != nil {
		return results, fmt.Errorf("%w: %w", ErrOrgAPI, err)
	}

	accountIDs = v.applyFilter(accountIDs)

	// Region list is static — no EC2 DescribeRegions call needed.
	var regions []string
	if onRegion != nil && len(accountIDs) > 0 {
		regions = internal.AllowedRegions(includeGov)
	}

	v.logger.Info("starting org visit",
		"accounts", len(accountIDs),
		"regions", len(regions),
		"concurrency", v.concurrency,
		"env", env,
	)

	// Pre-allocate result slots; goroutines write into these with mutex protection.
	var mu sync.Mutex
	for _, id := range accountIDs {
		results.Accounts[id] = &AccountResult{
			AccountID: id,
			Regions:   make(map[string]*RegionResult),
		}
	}

	sem := make(chan struct{}, v.concurrency)
	var wg sync.WaitGroup

	for _, id := range accountIDs {
		wg.Add(1)
		sem <- struct{}{} // acquire slot
		go func(accountID string) {
			defer wg.Done()
			defer func() { <-sem }() // release slot
			v.visitAccount(ctx, accountID, homeRegion, regions, onAccount, onRegion, &mu, &results)
		}(id)
	}
	wg.Wait()

	for _, ar := range results.Accounts {
		if ar.Err != nil {
			results.TotalErrors++
		}
		for _, rr := range ar.Regions {
			if rr.Err != nil {
				results.TotalErrors++
			}
		}
	}
	results.TimeElapsed = time.Since(start)
	v.logger.Info("org visit complete",
		"elapsed", results.TimeElapsed,
		"total_errors", results.TotalErrors,
		"success_rate", results.SuccessRate(),
	)
	return results, nil
}

func (v *OrgVisitor) visitAccount(
	ctx context.Context,
	accountID, homeRegion string,
	regions []string,
	onAccount AccountVisitor,
	onRegion RegionVisitor,
	mu *sync.Mutex,
	results *VisitResults,
) {
	assumedCfg, err := v.assumeRole(ctx, v.baseCfg, accountID, homeRegion, v.roleName)
	if err != nil {
		v.logger.Error("assume role failed", "account", accountID, "err", err)
		mu.Lock()
		results.Accounts[accountID].Err = fmt.Errorf("%w in %s: %w", ErrAssumeRole, accountID, err)
		mu.Unlock()
		return
	}

	v.logger.Info("visiting account", "account", accountID)

	if onAccount != nil {
		result, err := onAccount(ctx, assumedCfg, accountID)
		mu.Lock()
		results.Accounts[accountID].Result = result
		results.Accounts[accountID].Err = err
		mu.Unlock()
		if err != nil {
			v.logger.Error("account visitor error", "account", accountID, "err", err)
		}
	}

	if onRegion == nil {
		return
	}

	// Regions within an account are visited in parallel (unlimited within the account
	// goroutine — account-level concurrency is already bounded by the semaphore above).
	var regionWG sync.WaitGroup
	for _, region := range regions {
		regionWG.Add(1)
		go func(r string) {
			defer regionWG.Done()
			// Copy the assumed config and override the region.
			regionalCfg := assumedCfg.Copy()
			regionalCfg.Region = r

			v.logger.Debug("visiting region", "account", accountID, "region", r)
			result, err := onRegion(ctx, regionalCfg, accountID, r)

			mu.Lock()
			results.Accounts[accountID].Regions[r] = &RegionResult{
				Region: r,
				Result: result,
				Err:    err,
			}
			mu.Unlock()

			if err != nil {
				v.logger.Error("region visitor error", "account", accountID, "region", r, "err", err)
			}
		}(region)
	}
	regionWG.Wait()
}

// DryRun returns the accounts and regions that would be visited without
// calling any visitor functions or assuming any roles.
func (v *OrgVisitor) DryRun(
	ctx context.Context,
	env string,
	parentID string,
) (accountIDs []string, regions []string, err error) {
	homeRegion, includeGov, err := envConfig(env)
	if err != nil {
		return nil, nil, err
	}

	orgCfg := v.baseCfg.Copy()
	orgCfg.Region = homeRegion
	accountIDs, err = internal.ListAccounts(ctx, v.newOrgClient(orgCfg), parentID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrOrgAPI, err)
	}

	accountIDs = v.applyFilter(accountIDs)
	regions = internal.AllowedRegions(includeGov)
	return accountIDs, regions, nil
}

// applyFilter removes accounts for which v.filter returns true.
func (v *OrgVisitor) applyFilter(ids []string) []string {
	if v.filter == nil {
		return ids
	}
	out := ids[:0]
	for _, id := range ids {
		if !v.filter(id) {
			out = append(out, id)
		}
	}
	return out
}

// envConfig returns the home region and includeGov flag for env ("com" or "gov").
func envConfig(env string) (homeRegion string, includeGov bool, err error) {
	switch env {
	case "com":
		return "us-east-1", false, nil
	case "gov":
		return "us-gov-west-1", true, nil
	default:
		return "", false, fmt.Errorf("%w: got %q", ErrInvalidEnv, env)
	}
}
