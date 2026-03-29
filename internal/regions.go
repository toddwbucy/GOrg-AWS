package internal

// AWS partition identifiers.
const (
	PartitionCOM = "aws"
	PartitionGOV = "aws-us-gov"
)

// allowedCOM and allowedGOV are the only regions supported by CloudOpsTools.
// Dynamic region discovery (ec2.DescribeRegions) is intentionally not used —
// the allowed set is fixed to CONUS commercial and GovCloud regions.
var (
	allowedCOM = []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2"}
	allowedGOV = []string{"us-gov-east-1", "us-gov-west-1"}
)

// AllowedRegions returns the fixed set of regions for the given partition.
// includeGov=false → four CONUS commercial regions.
// includeGov=true  → two GovCloud regions.
func AllowedRegions(includeGov bool) []string {
	if includeGov {
		out := make([]string, len(allowedGOV))
		copy(out, allowedGOV)
		return out
	}
	out := make([]string, len(allowedCOM))
	copy(out, allowedCOM)
	return out
}
