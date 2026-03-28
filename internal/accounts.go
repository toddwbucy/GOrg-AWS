package internal

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

// OrgLister is the subset of *organizations.Client used for account listing.
// Accepting an interface (rather than the concrete client) keeps tests fast —
// no network calls, no credentials required.
type OrgLister interface {
	ListAccounts(ctx context.Context, params *organizations.ListAccountsInput, optFns ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error)
	ListAccountsForParent(ctx context.Context, params *organizations.ListAccountsForParentInput, optFns ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error)
}

// ListAccounts returns all ACTIVE account IDs in the organization.
// If parentID is non-empty, only accounts directly under that OU are returned.
// Pagination is handled transparently.
func ListAccounts(ctx context.Context, lister OrgLister, parentID string) ([]string, error) {
	if parentID != "" {
		return listForParent(ctx, lister, parentID)
	}
	return listAll(ctx, lister)
}

func listAll(ctx context.Context, lister OrgLister) ([]string, error) {
	var ids []string
	var nextToken *string
	for {
		out, err := lister.ListAccounts(ctx, &organizations.ListAccountsInput{
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("organizations.ListAccounts: %w", err)
		}
		for _, a := range out.Accounts {
			if a.Status == types.AccountStatusActive && a.Id != nil {
				ids = append(ids, *a.Id)
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return ids, nil
}

func listForParent(ctx context.Context, lister OrgLister, parentID string) ([]string, error) {
	var ids []string
	var nextToken *string
	for {
		out, err := lister.ListAccountsForParent(ctx, &organizations.ListAccountsForParentInput{
			ParentId:  &parentID,
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("organizations.ListAccountsForParent: %w", err)
		}
		for _, a := range out.Accounts {
			if a.Status == types.AccountStatusActive && a.Id != nil {
				ids = append(ids, *a.Id)
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return ids, nil
}
