package internal

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

// AWS partition identifiers.
const (
	PartitionCOM = "aws"
	PartitionGOV = "aws-us-gov"
)

// RegionDescriber is the subset of *ec2.Client used for region discovery.
type RegionDescriber interface {
	DescribeRegions(ctx context.Context, params *ec2.DescribeRegionsInput, optFns ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error)
}

// GetUSRegions returns the names of enabled US regions visible to the caller.
// includeGov controls whether us-gov-* regions are included.
// Only regions starting with "us-" are returned — commercial and GovCloud.
func GetUSRegions(ctx context.Context, describer RegionDescriber, includeGov bool) ([]string, error) {
	out, err := describer.DescribeRegions(ctx, &ec2.DescribeRegionsInput{
		// AllRegions false → only regions enabled for this account.
		AllRegions: aws.Bool(false),
	})
	if err != nil {
		return nil, fmt.Errorf("ec2.DescribeRegions: %w", err)
	}

	var regions []string
	for _, r := range out.Regions {
		if r.RegionName == nil {
			continue
		}
		name := *r.RegionName
		if !strings.HasPrefix(name, "us-") {
			continue
		}
		// When includeGov is false, skip GovCloud regions.
		// Mirrors the Python original: include_gov=True returns all us-* regions.
		if !includeGov && strings.HasPrefix(name, "us-gov-") {
			continue
		}
		regions = append(regions, name)
	}
	return regions, nil
}
