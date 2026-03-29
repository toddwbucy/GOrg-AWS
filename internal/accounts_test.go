package internal

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	"github.com/aws/aws-sdk-go-v2/service/organizations/types"
)

type stubOrgLister struct {
	pages          [][]types.Account // multi-page responses for ListAccounts
	pageIdx        int
	forParentPages [][]types.Account // multi-page responses for ListAccountsForParent
	fpIdx          int
	// ousByParent maps parentID → child OUs for ListOrganizationalUnitsForParent
	ousByParent map[string][]types.OrganizationalUnit
	// accountsByParent overrides forParentPages when set, keyed by parentID
	accountsByParent map[string][]types.Account
	err              error
}

func (s *stubOrgLister) ListAccounts(_ context.Context, _ *organizations.ListAccountsInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.pageIdx >= len(s.pages) {
		return &organizations.ListAccountsOutput{}, nil
	}
	page := s.pages[s.pageIdx]
	s.pageIdx++
	out := &organizations.ListAccountsOutput{Accounts: page}
	if s.pageIdx < len(s.pages) {
		tok := "next"
		out.NextToken = &tok
	}
	return out, nil
}

func (s *stubOrgLister) ListAccountsForParent(_ context.Context, in *organizations.ListAccountsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsForParentOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	// Use per-parent map if populated.
	if s.accountsByParent != nil {
		accts := s.accountsByParent[aws.ToString(in.ParentId)]
		return &organizations.ListAccountsForParentOutput{Accounts: accts}, nil
	}
	// Fall back to the simple pages slice.
	if s.fpIdx >= len(s.forParentPages) {
		return &organizations.ListAccountsForParentOutput{}, nil
	}
	page := s.forParentPages[s.fpIdx]
	s.fpIdx++
	out := &organizations.ListAccountsForParentOutput{Accounts: page}
	if s.fpIdx < len(s.forParentPages) {
		tok := "next"
		out.NextToken = &tok
	}
	return out, nil
}

func (s *stubOrgLister) ListOrganizationalUnitsForParent(_ context.Context, in *organizations.ListOrganizationalUnitsForParentInput, _ ...func(*organizations.Options)) (*organizations.ListOrganizationalUnitsForParentOutput, error) {
	if s.err != nil {
		return nil, s.err
	}
	ous := s.ousByParent[aws.ToString(in.ParentId)]
	return &organizations.ListOrganizationalUnitsForParentOutput{OrganizationalUnits: ous}, nil
}

func mkAccount(id string, state types.AccountState) types.Account {
	return types.Account{Id: aws.String(id), State: state}
}

func mkOU(id string) types.OrganizationalUnit {
	return types.OrganizationalUnit{Id: aws.String(id)}
}

func TestListAccounts_AllOrg(t *testing.T) {
	stub := &stubOrgLister{
		pages: [][]types.Account{
			{
				mkAccount("111111111111", types.AccountStateActive),
				mkAccount("222222222222", types.AccountStateActive),
			},
			{
				mkAccount("333333333333", types.AccountStateActive),
				mkAccount("444444444444", types.AccountStateSuspended), // should be skipped
			},
		},
	}

	ids, err := ListAccounts(context.Background(), stub, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("got %d accounts, want 3 (suspended excluded); ids=%v", len(ids), ids)
	}
	for _, id := range []string{"111111111111", "222222222222", "333333333333"} {
		found := false
		for _, got := range ids {
			if got == id {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected account %s in results", id)
		}
	}
}

func TestListAccounts_ForParent_DirectOnly(t *testing.T) {
	stub := &stubOrgLister{
		forParentPages: [][]types.Account{
			{mkAccount("555555555555", types.AccountStateActive)},
		},
	}

	ids, err := ListAccounts(context.Background(), stub, "ou-xxxx-xxxxxxxx")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "555555555555" {
		t.Errorf("got %v, want [555555555555]", ids)
	}
}

// TestListAccounts_ForParent_Nested verifies that accounts in child OUs are
// included when a parentID is given.
//
// Tree:
//
//	ou-root
//	├── 111111111111  (direct account)
//	└── ou-child
//	    ├── 222222222222
//	    └── ou-grandchild
//	        └── 333333333333
func TestListAccounts_ForParent_Nested(t *testing.T) {
	stub := &stubOrgLister{
		accountsByParent: map[string][]types.Account{
			"ou-root":       {mkAccount("111111111111", types.AccountStateActive)},
			"ou-child":      {mkAccount("222222222222", types.AccountStateActive)},
			"ou-grandchild": {mkAccount("333333333333", types.AccountStateActive)},
		},
		ousByParent: map[string][]types.OrganizationalUnit{
			"ou-root":  {mkOU("ou-child")},
			"ou-child": {mkOU("ou-grandchild")},
		},
	}

	ids, err := ListAccounts(context.Background(), stub, "ou-root")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 3 {
		t.Errorf("got %d accounts, want 3: %v", len(ids), ids)
	}
	want := map[string]bool{"111111111111": true, "222222222222": true, "333333333333": true}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected account ID %q in results", id)
		}
	}
}

func TestListAccounts_Error(t *testing.T) {
	stub := &stubOrgLister{err: errors.New("unauthorized")}
	_, err := ListAccounts(context.Background(), stub, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
