# gorg-aws

Go module implementing the AWS Organization Visitor pattern — a type-safe, concurrent equivalent of the internal Python `org_visitor` library.

Callers supply visitor functions; `gorg-aws` handles Organizations API pagination, STS role assumption, and region discovery. Visitor functions receive a pre-configured `aws.Config` and never touch credentials or ARNs directly.

```bash
go get github.com/toddwbucy/gorg-aws
```

---

## Quick Start

```go
import (
    "context"
    "github.com/aws/aws-sdk-go-v2/aws"
    "github.com/aws/aws-sdk-go-v2/config"
    gorgaws "github.com/toddwbucy/gorg-aws"
)

cfg, err := config.LoadDefaultConfig(ctx)
if err != nil {
    log.Fatalf("load config: %v", err)
}

v := gorgaws.New(cfg,
    gorgaws.WithConcurrency(20),
    gorgaws.WithRoleName("OrganizationAccountAccessRole"),
)

results, err := v.VisitOrganization(ctx, "com",
    // AccountVisitor — called once per account
    func(ctx context.Context, cfg aws.Config, accountID string) (any, error) {
        // cfg already has assumed-role credentials — use it directly
        return doAccountWork(ctx, cfg, accountID)
    },
    // RegionVisitor — called once per account+region
    func(ctx context.Context, cfg aws.Config, accountID, region string) (any, error) {
        return doRegionalWork(ctx, cfg, accountID, region)
    },
    "", // parentID: "" = entire org, or provide an OU ID
)
```

---

## Python Comparison

The Go API maps directly to the Python original:

```text
Python (existing):                          Go (gorg-aws):
──────────────────────────────────────────  ──────────────────────────────────────────
visit_organization(                         v.VisitOrganization(
  environment="gov",                            ctx, "gov",
  account_visitor=fn,                           onAccount,
  region_visitor=fn,                            onRegion,
)                                               "",
                                            )

account_visitor(session, account_id)        AccountVisitor(ctx, cfg, accountID)
region_visitor(session, region, account_id) RegionVisitor(ctx, cfg, accountID, region)

boto3.Session → pre-assumed credentials     aws.Config  → pre-assumed credentials
Sequential traversal                        Concurrent  (default: 10 accounts parallel)
No dry-run capability                       DryRun() — preview scope before visiting
```

Key behavioral difference: the Python version makes role assumption calls sequentially
(one account, then its regions, then the next account). With 20 accounts × 8 regions,
the Python version makes 160 sequential operation groups. The Go version processes up to
10 accounts at once, and visits all regions within an account in parallel, reducing
wall-clock time to roughly the slowest single account.

---

## DryRun

`DryRun` returns the accounts and regions that *would* be visited without calling any
visitor functions or assuming any roles. Use it to validate scope before running
expensive or side-effecting operations:

```go
accounts, regions, err := v.DryRun(ctx, "com", "ou-xxxx-xxxxxxxx")
if err != nil {
    log.Fatal(err)
}
fmt.Printf("Would visit %d accounts across %d regions\n", len(accounts), len(regions))
// Proceed only if scope looks correct.
```

---

## Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithConcurrency(n)` | `10` | Max accounts processed concurrently |
| `WithRoleName(name)` | `"OrganizationAccountAccessRole"` | IAM role assumed in each account |
| `WithLogger(l)` | `slog.Default()` | Structured logger for visit progress |
| `WithAccountFilter(fn)` | none | Skip accounts where fn returns true |

```go
v := gorgaws.New(cfg,
    gorgaws.WithConcurrency(20),
    gorgaws.WithRoleName("MyCustomCrossAccountRole"),
    gorgaws.WithLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))),
    gorgaws.WithAccountFilter(func(id string) bool {
        return id == "123456789012" // skip management account
    }),
)
```

---

## Result Types

```go
type VisitResults struct {
    Accounts    map[string]*AccountResult
    TimeElapsed time.Duration
    TotalErrors int
}

// Helper methods
results.SuccessfulAccounts() []*AccountResult
results.FailedAccounts()     []*AccountResult
results.SuccessRate()        float64
```

Per-account errors (failed role assumption, visitor error) are recorded in
`AccountResult.Err` and do not abort the walk. A non-nil error from
`VisitOrganization` itself indicates a fatal failure: invalid credentials,
Organizations API unreachable, or region discovery failure.

---

## Error Handling

```go
results, err := v.VisitOrganization(ctx, "com", onAccount, onRegion, "")
if err != nil {
    // Fatal: couldn't start the walk at all
    if errors.Is(err, gorgaws.ErrInvalidEnv) { ... }
    if errors.Is(err, gorgaws.ErrOrgAPI)     { ... }
}

// Per-account errors
for _, failed := range results.FailedAccounts() {
    if errors.Is(failed.Err, gorgaws.ErrAssumeRole) {
        // This account's role assumption failed
    }
}
```

Sentinel errors:

| Error | Meaning |
|-------|---------|
| `ErrNoCredentials` | No credentials available for the environment |
| `ErrAssumeRole` | STS AssumeRole failed for a target account |
| `ErrOrgAPI` | Organizations API returned an unexpected error |
| `ErrRegionAPI` | EC2 DescribeRegions failed |
| `ErrInvalidEnv` | env was not `"com"` or `"gov"` |

---

## GovCloud

Pass `env: "gov"` to target the GovCloud partition:

```go
results, err := v.VisitOrganization(ctx, "gov", onAccount, onRegion, "")
```

The module handles the partition differences automatically:
- STS endpoint: `sts.us-gov-west-1.amazonaws.com`
- IAM ARN format: `arn:aws-us-gov:iam::ACCOUNT:role/ROLE`
- Home region for Organizations/EC2 discovery: `us-gov-west-1`
- Region filter: `us-gov-*` only

---

## Supply Chain Security

This module has one class of external dependency: `github.com/aws/aws-sdk-go-v2/*`,
maintained directly by AWS. No third-party dependencies.

Verify your build:

```bash
go mod verify
grep -v "^github.com/aws" go.sum  # should return nothing
```

This module compiles to a static binary. There is no runtime dependency loading,
no site-packages directory, and no equivalent of Python's `.pth` file attack surface.

The supply chain attack that compromised LiteLLM on 2026-03-24 (TeamPCP rewrote a
`trivy-action` Git tag, exfiltrated a PyPI publish token, then pushed backdoored
`litellm` versions containing a `.pth` file that executed on every Python process
startup) cannot affect a deployed binary built from this module. The attack surface
for gorg-aws is the build pipeline only, not every server running the binary.

---

## Examples

- [`examples/list_instances`](examples/list_instances/main.go) — count running EC2 instances per account/region
- [`examples/org_inventory`](examples/org_inventory/main.go) — dry-run preview then full org visit

---

## Module Path

```text
github.com/toddwbucy/gorg-aws
```

Part of the `gorg-*` family of cloud organization visitor modules:
- `gorg-aws` — AWS Organizations (this module)
- `gorg-azure` — Azure Management Groups (planned)
- `gorg-gcp` — GCP Resource Manager (planned)

Each module is independent — separate `go.sum`, separate release cadence, no cross-cloud SDK surface area.
