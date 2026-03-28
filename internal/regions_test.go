package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

type stubRegionDescriber struct {
	regions []string
	err     error
}

func (s *stubRegionDescriber) DescribeRegions(_ context.Context, _ *ec2.DescribeRegionsInput, _ ...func(*ec2.Options)) (*ec2.DescribeRegionsOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := &ec2.DescribeRegionsOutput{}
	for _, r := range s.regions {
		out.Regions = append(out.Regions, ec2types.Region{RegionName: aws.String(r)})
	}
	return out, nil
}

var allRegions = []string{
	"us-east-1",
	"us-east-2",
	"us-west-1",
	"us-west-2",
	"us-gov-west-1",
	"us-gov-east-1",
	"eu-west-1",      // non-US, should always be excluded
	"ap-southeast-1", // non-US, should always be excluded
}

func TestGetUSRegions_ExcludeGov(t *testing.T) {
	stub := &stubRegionDescriber{regions: allRegions}
	got, err := GetUSRegions(context.Background(), stub, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range got {
		if r == "eu-west-1" || r == "ap-southeast-1" {
			t.Errorf("non-US region %s should not be returned", r)
		}
		if r == "us-gov-west-1" || r == "us-gov-east-1" {
			t.Errorf("GovCloud region %s should not be returned when includeGov=false", r)
		}
	}
	// Should have the 4 commercial US regions.
	if len(got) != 4 {
		t.Errorf("got %d regions, want 4: %v", len(got), got)
	}
}

func TestGetUSRegions_IncludeGov(t *testing.T) {
	stub := &stubRegionDescriber{regions: allRegions}
	got, err := GetUSRegions(context.Background(), stub, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, r := range got {
		if r == "eu-west-1" || r == "ap-southeast-1" {
			t.Errorf("non-US region %s should not be returned", r)
		}
	}
	// All 6 US regions (4 commercial + 2 gov) should be present.
	if len(got) != 6 {
		t.Errorf("got %d regions, want 6: %v", len(got), got)
	}
}

func TestGetUSRegions_Error(t *testing.T) {
	stub := &stubRegionDescriber{err: errors.New("throttled")}
	_, err := GetUSRegions(context.Background(), stub, false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
