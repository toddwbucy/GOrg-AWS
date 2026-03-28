// list_instances visits every account and region in the organization and
// prints the running EC2 instance count per region. Demonstrates the
// RegionVisitor pattern with a real AWS API call.
//
// Usage:
//
//	AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_SESSION_TOKEN=... \
//	  go run ./examples/list_instances --env com
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	gorgaws "github.com/toddwbucy/gorg-aws"
)

// regionSummary is returned by the RegionVisitor.
type regionSummary struct {
	InstanceCount int
}

func main() {
	env := flag.String("env", "com", "AWS environment: com or gov")
	parentID := flag.String("parent", "", "optional OU ID to scope traversal")
	flag.Parse()

	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	v := gorgaws.New(cfg,
		gorgaws.WithConcurrency(10),
		gorgaws.WithRoleName("OrganizationAccountAccessRole"),
	)

	results, err := v.VisitOrganization(ctx, *env,
		// AccountVisitor — called once per account.
		// cfg already has assumed-role credentials; callers never touch auth.
		func(_ context.Context, _ aws.Config, accountID string) (any, error) {
			fmt.Printf("[account] %s\n", accountID)
			return nil, nil
		},

		// RegionVisitor — called once per account+region.
		// cfg.Region is already set to the target region.
		func(ctx context.Context, cfg aws.Config, accountID, region string) (any, error) {
			ec2Client := ec2.NewFromConfig(cfg)

			var count int
			var nextToken *string
			for {
				out, err := ec2Client.DescribeInstances(ctx, &ec2.DescribeInstancesInput{
					Filters: []ec2types.Filter{
						{
							Name:   aws.String("instance-state-name"),
							Values: []string{"running"},
						},
					},
					NextToken: nextToken,
				})
				if err != nil {
					return nil, fmt.Errorf("DescribeInstances: %w", err)
				}
				for _, r := range out.Reservations {
					count += len(r.Instances)
				}
				if out.NextToken == nil {
					break
				}
				nextToken = out.NextToken
			}

			return &regionSummary{InstanceCount: count}, nil
		},

		*parentID,
	)
	if err != nil {
		log.Fatalf("VisitOrganization: %v", err)
	}

	fmt.Println()
	fmt.Println("=== Running EC2 instances by account/region ===")

	// Sort for deterministic output.
	accountIDs := make([]string, 0, len(results.Accounts))
	for id := range results.Accounts {
		accountIDs = append(accountIDs, id)
	}
	sort.Strings(accountIDs)

	for _, id := range accountIDs {
		ar := results.Accounts[id]
		if ar.Err != nil {
			fmt.Printf("  %s  ERROR: %v\n", id, ar.Err)
			continue
		}
		fmt.Printf("  %s\n", id)
		regions := make([]string, 0, len(ar.Regions))
		for r := range ar.Regions {
			regions = append(regions, r)
		}
		sort.Strings(regions)
		for _, r := range regions {
			rr := ar.Regions[r]
			if rr.Err != nil {
				fmt.Printf("    %-20s  ERROR: %v\n", r, rr.Err)
				continue
			}
			summary, _ := rr.Result.(*regionSummary)
			if summary != nil {
				fmt.Printf("    %-20s  %d running\n", r, summary.InstanceCount)
			}
		}
	}

	fmt.Printf("\nCompleted in %s — %d accounts, %d errors\n",
		results.TimeElapsed,
		len(results.Accounts),
		results.TotalErrors,
	)
}
