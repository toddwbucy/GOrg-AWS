package internal

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// AssumedConfig returns an aws.Config pre-loaded with assumed-role credentials
// for the given accountID and region. Callers never touch ARNs, tokens, or STS.
//
// stscreds.NewAssumeRoleProvider + aws.NewCredentialsCache handles:
//   - Initial STS AssumeRole call
//   - Automatic credential refresh before the 1-hour expiry
//   - No manual AccessKeyId/SecretAccessKey extraction (unlike the Python original)
func AssumedConfig(ctx context.Context, baseCfg aws.Config, accountID, region, roleName string) (aws.Config, error) {
	partition := PartitionCOM
	if strings.HasPrefix(region, "us-gov-") {
		partition = PartitionGOV
	}
	roleARN := fmt.Sprintf("arn:%s:iam::%s:role/%s", partition, accountID, roleName)

	stsClient := sts.NewFromConfig(baseCfg)
	provider := stscreds.NewAssumeRoleProvider(stsClient, roleARN,
		func(o *stscreds.AssumeRoleOptions) {
			o.RoleSessionName = "gorgaws-OrgVisitor"
		},
	)

	cfg, err := config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(aws.NewCredentialsCache(provider)),
		config.WithRegion(region),
	)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load assumed config for %s: %w", accountID, err)
	}
	return cfg, nil
}
