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
	ListOrganizationalUnitsForParent(ctx context.Context, params *organizations.ListOrganizationalUnitsForParentInput, optFns ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error)
}

// ListAccounts returns all ACTIVE account IDs in the organization.
// If parentID is non-empty, all accounts under that OU are returned,
// including accounts in nested child OUs (recursive traversal).
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
			if a.State == types.AccountStateActive && a.Id != nil {
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

// listForParent recursively collects all active accounts under parentID,
// including accounts in nested child OUs. AWS Organizations is a strict tree
// (no cycles), so simple DFS is safe.
func listForParent(ctx context.Context, lister OrgLister, parentID string) ([]string, error) {
	ids, err := listDirectAccounts(ctx, lister, parentID)
	if err != nil {
		return nil, err
	}

	ouIDs, err := listDirectOUs(ctx, lister, parentID)
	if err != nil {
		return nil, err
	}
	for _, ouID := range ouIDs {
		nested, err := listForParent(ctx, lister, ouID)
		if err != nil {
			return nil, err
		}
		ids = append(ids, nested...)
	}
	return ids, nil
}

// listDirectAccounts returns IDs of active accounts that are direct children of parentID.
func listDirectAccounts(ctx context.Context, lister OrgLister, parentID string) ([]string, error) {
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
			if a.State == types.AccountStateActive && a.Id != nil {
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

// listDirectOUs returns IDs of OUs that are direct children of parentID.
func listDirectOUs(ctx context.Context, lister OrgLister, parentID string) ([]string, error) {
	var ouIDs []string
	var nextToken *string
	for {
		out, err := lister.ListOrganizationalUnitsForParent(ctx, &organizations.ListOrganizationalUnitsForParentInput{
			ParentId:  &parentID,
			NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("organizations.ListOrganizationalUnitsForParent: %w", err)
		}
		for _, ou := range out.OrganizationalUnits {
			if ou.Id != nil {
				ouIDs = append(ouIDs, *ou.Id)
			}
		}
		if out.NextToken == nil {
			break
		}
		nextToken = out.NextToken
	}
	return ouIDs, nil
}
