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
	"github.com/aws/aws-sdk-go-v2/service/ec2"
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
	newEC2Client func(aws.Config) internal.RegionDescriber
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
		newEC2Client: func(cfg aws.Config) internal.RegionDescriber {
			return ec2.NewFromConfig(cfg)
		},
		assumeRole: internal.AssumedConfig,
	}
	for _, o := range opts {
		o(v)
	}
	return v
}

// VisitOrganization walks every account in the organization (or under parentID)
// and calls onAccount and onRegion with pre-assumed credentials for each.
//
// env must be "com" or "gov". parentID may be "" to walk the full organization.
// Either onAccount or onRegion may be nil to skip that visitor type.
//
// Errors from individual accounts are recorded in VisitResults.Accounts[id].Err
// and do not abort the walk. A non-nil returned error indicates a fatal failure
// (credentials, Organizations API, or region discovery).
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
		return results, fmt.Errorf("%w: %s", ErrOrgAPI, err)
	}

	// Apply filter before region discovery: no need to call EC2 if there are
	// no accounts to visit, or if no RegionVisitor was provided.
	accountIDs = v.applyFilter(accountIDs)

	var regions []string
	if onRegion != nil && len(accountIDs) > 0 {
		ec2Cfg := v.baseCfg.Copy()
		ec2Cfg.Region = homeRegion
		regions, err = internal.GetUSRegions(ctx, v.newEC2Client(ec2Cfg), includeGov)
		if err != nil {
			return results, fmt.Errorf("%w: %s", ErrRegionAPI, err)
		}
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
		results.Accounts[accountID].Err = fmt.Errorf("%w in %s: %s", ErrAssumeRole, accountID, err)
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
// calling any visitor functions. Use this to validate org scope before
// running expensive operations — no equivalent in the Python original.
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
		return nil, nil, fmt.Errorf("%w: %s", ErrOrgAPI, err)
	}

	// Apply filter before region discovery — no need to call EC2 if all
	// accounts are filtered out. Mirrors the guard in VisitOrganization.
	accountIDs = v.applyFilter(accountIDs)
	if len(accountIDs) == 0 {
		return accountIDs, nil, nil
	}

	ec2Cfg := v.baseCfg.Copy()
	ec2Cfg.Region = homeRegion
	regions, err = internal.GetUSRegions(ctx, v.newEC2Client(ec2Cfg), includeGov)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrRegionAPI, err)
	}

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
