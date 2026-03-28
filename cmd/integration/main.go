// integration is a manual test harness for gorg-aws against a real AWS organization.
//
// It proves end-to-end function by:
//   - Assuming OrganizationAccountAccessRole (or a custom role) in every account
//   - Calling STS GetCallerIdentity per account  → verifies role assumption succeeded
//   - Calling EC2 DescribeAvailabilityZones per region → verifies regional API access
//
// Usage:
//
//	cp testconfig.json.example testconfig.json   # fill in your creds
//	go run ./cmd/integration --config cmd/integration/testconfig.json
//	go run ./cmd/integration --config cmd/integration/testconfig.json --dryrun
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	gorgaws "github.com/toddwbucy/gorg-aws"
)

// testConfig is the structure of testconfig.json.
// Credentials may also come from environment variables (AWS_ACCESS_KEY_ID etc.)
// if access_key_id is left blank in the file.
type testConfig struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`

	Env         string `json:"env"`
	RoleName    string `json:"role_name"`
	ParentID    string `json:"parent_id"`
	Concurrency int    `json:"concurrency"`
}

// accountProbe is returned by the AccountVisitor.
type accountProbe struct {
	CallerARN string
	UserID    string
}

// regionProbe is returned by the RegionVisitor.
type regionProbe struct {
	AZCount int
	AZNames []string
}

func main() {
	cfgFile := flag.String("config", "cmd/integration/testconfig.json", "path to testconfig.json")
	dryrun := flag.Bool("dryrun", false, "list scope without visiting")
	flag.Parse()

	tc, err := loadTestConfig(*cfgFile)
	if err != nil {
		fatalf("load config: %v\n", err)
	}

	ctx := context.Background()
	baseCfg, err := buildAWSConfig(ctx, tc)
	if err != nil {
		fatalf("build AWS config: %v\n", err)
	}

	opts := []gorgaws.Option{
		gorgaws.WithRoleName(tc.RoleName),
	}
	if tc.Concurrency > 0 {
		opts = append(opts, gorgaws.WithConcurrency(tc.Concurrency))
	}

	v := gorgaws.New(baseCfg, opts...)

	printBanner(tc)

	// Always dry-run first to show scope.
	accounts, regions, err := v.DryRun(ctx, tc.Env, tc.ParentID)
	if err != nil {
		fatalf("dry run: %v\n", err)
	}
	printDryRun(accounts, regions)

	if *dryrun {
		return
	}

	if !confirm("Proceed with full visit?") {
		fmt.Println("aborted")
		return
	}

	fmt.Println()
	results, err := v.VisitOrganization(ctx, tc.Env,
		func(ctx context.Context, cfg aws.Config, accountID string) (any, error) {
			c := sts.NewFromConfig(cfg)
			out, err := c.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
			if err != nil {
				return nil, fmt.Errorf("GetCallerIdentity: %w", err)
			}
			return &accountProbe{
				CallerARN: aws.ToString(out.Arn),
				UserID:    aws.ToString(out.UserId),
			}, nil
		},
		func(ctx context.Context, cfg aws.Config, accountID, region string) (any, error) {
			c := ec2.NewFromConfig(cfg)
			out, err := c.DescribeAvailabilityZones(ctx, &ec2.DescribeAvailabilityZonesInput{})
			if err != nil {
				return nil, fmt.Errorf("DescribeAvailabilityZones: %w", err)
			}
			probe := &regionProbe{}
			for _, az := range out.AvailabilityZones {
				probe.AZCount++
				probe.AZNames = append(probe.AZNames, aws.ToString(az.ZoneName))
			}
			return probe, nil
		},
		tc.ParentID,
	)
	if err != nil {
		fatalf("VisitOrganization: %v\n", err)
	}

	printResults(results)
}

// ── output ────────────────────────────────────────────────────────────────

func printBanner(tc *testConfig) {
	fmt.Println("=== gorg-aws integration test ===")
	fmt.Printf("env:         %s\n", tc.Env)
	fmt.Printf("role:        %s\n", tc.RoleName)
	parent := tc.ParentID
	if parent == "" {
		parent = "(entire org)"
	}
	fmt.Printf("parent:      %s\n", parent)
	fmt.Printf("concurrency: %d\n", tc.Concurrency)
	fmt.Println()
}

func printDryRun(accounts, regions []string) {
	fmt.Printf("Dry run scope:\n")
	fmt.Printf("  Accounts : %d\n", len(accounts))
	fmt.Printf("  Regions  : %d  [%s]\n", len(regions), strings.Join(regions, ", "))
	fmt.Println()
}

func printResults(results gorgaws.VisitResults) {
	// Sort accounts for deterministic output.
	ids := make([]string, 0, len(results.Accounts))
	for id := range results.Accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Println("─────────────────────────────────────────────────────")
	for _, id := range ids {
		ar := results.Accounts[id]
		if ar.Err != nil {
			fmt.Printf("  ✗ %s  ERROR: %v\n", id, ar.Err)
			continue
		}

		probe, _ := ar.Result.(*accountProbe)
		if probe != nil {
			fmt.Printf("  ✓ %s  %s\n", id, probe.CallerARN)
		} else {
			fmt.Printf("  ✓ %s\n", id)
		}

		// Sort regions within the account.
		rnames := make([]string, 0, len(ar.Regions))
		for r := range ar.Regions {
			rnames = append(rnames, r)
		}
		sort.Strings(rnames)

		for _, r := range rnames {
			rr := ar.Regions[r]
			if rr.Err != nil {
				fmt.Printf("      ✗ %-20s  ERROR: %v\n", r, rr.Err)
				continue
			}
			rp, _ := rr.Result.(*regionProbe)
			if rp != nil {
				fmt.Printf("      ✓ %-20s  %d AZs  [%s]\n", r, rp.AZCount, strings.Join(rp.AZNames, ", "))
			}
		}
	}
	fmt.Println("─────────────────────────────────────────────────────")

	total := len(results.Accounts)
	failed := len(results.FailedAccounts())
	passed := total - failed

	fmt.Printf("\nAccounts : %d/%d passed\n", passed, total)
	fmt.Printf("Errors   : %d\n", results.TotalErrors)
	fmt.Printf("Rate     : %.0f%%\n", results.SuccessRate()*100)
	fmt.Printf("Elapsed  : %s\n", results.TimeElapsed.Round(time.Millisecond))

	if failed > 0 {
		os.Exit(1)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────

func loadTestConfig(path string) (*testConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w\n\nCopy cmd/integration/testconfig.json.example to %s and fill in your values.", path, err, path)
	}
	defer f.Close()

	var tc testConfig
	if err := json.NewDecoder(f).Decode(&tc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	if tc.Env == "" {
		tc.Env = "com"
	}
	if tc.RoleName == "" {
		tc.RoleName = "OrganizationAccountAccessRole"
	}
	if tc.Concurrency == 0 {
		tc.Concurrency = 5
	}
	return &tc, nil
}

func buildAWSConfig(ctx context.Context, tc *testConfig) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	// If explicit creds are provided in the config file, use them.
	// Otherwise fall through to the standard credential chain
	// (env vars, ~/.aws/credentials, instance metadata, etc.).
	if tc.AccessKeyID != "" && tc.AccessKeyID != "AKIA..." {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(
				tc.AccessKeyID,
				tc.SecretAccessKey,
				tc.SessionToken,
			),
		))
	}

	return config.LoadDefaultConfig(ctx, opts...)
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	resp := strings.TrimSpace(scanner.Text())
	return strings.EqualFold(resp, "y")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format, args...)
	os.Exit(1)
}
