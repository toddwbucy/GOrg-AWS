// org_inventory prints a summary of all accounts in the organization and the
// enabled EC2 regions in each. It uses DryRun first so no visitor functions
// are run — this is the safe way to preview scope before a real operation.
//
// Usage:
//
//	AWS_ACCESS_KEY_ID=... AWS_SECRET_ACCESS_KEY=... AWS_SESSION_TOKEN=... \
//	  go run ./examples/org_inventory --env com
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	gorgaws "github.com/toddwbucy/gorg-aws"
)

func main() {
	env := flag.String("env", "com", "AWS environment: com or gov")
	parentID := flag.String("parent", "", "optional OU ID to scope traversal")
	concurrency := flag.Int("concurrency", 10, "max concurrent account visits")
	flag.Parse()

	ctx := context.Background()

	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "load config: %v\n", err)
		os.Exit(1)
	}

	v := gorgaws.New(cfg,
		gorgaws.WithConcurrency(*concurrency),
	)

	// DryRun: list what would be visited without assuming any roles.
	fmt.Printf("=== DryRun: env=%s parent=%q ===\n", *env, *parentID)
	accounts, regions, err := v.DryRun(ctx, *env, *parentID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dry run: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Accounts (%d):\n", len(accounts))
	for _, id := range accounts {
		fmt.Printf("  %s\n", id)
	}
	fmt.Printf("Regions (%d):\n", len(regions))
	for _, r := range regions {
		fmt.Printf("  %s\n", r)
	}
	fmt.Println()

	// Prompt before the real visit.
	fmt.Printf("Proceed with full visit? [y/N] ")
	var resp string
	fmt.Scan(&resp) //nolint:errcheck
	if resp != "y" && resp != "Y" {
		fmt.Println("aborted")
		os.Exit(0)
	}

	// Real visit: visit each account, collect results.
	results, err := v.VisitOrganization(ctx, *env,
		func(_ context.Context, _ aws.Config, accountID string) (any, error) {
			// account-level work — see list_instances example for a full implementation
			return map[string]string{"account": accountID}, nil
		},
		nil,
		*parentID,
	)
	if err != nil {
		log.Fatalf("visit: %v", err)
	}

	fmt.Printf("\nVisit complete: %d accounts, %d errors, %.0f%% success, elapsed %s\n",
		len(results.Accounts),
		results.TotalErrors,
		results.SuccessRate()*100,
		results.TimeElapsed,
	)
	for _, failed := range results.FailedAccounts() {
		fmt.Printf("  FAILED %s: %v\n", failed.AccountID, failed.Err)
	}
}
