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
	pages    [][]types.Account // multi-page responses
	pageIdx  int
	forParentPages [][]types.Account
	fpIdx    int
	err      error
}

func (s *stubOrgLister) ListAccounts(_ context.Context, in *organizations.ListAccountsInput, _ ...func(*organizations.Options)) (*organizations.ListAccountsOutput, error) {
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
	pages := s.forParentPages
	if s.fpIdx >= len(pages) {
		return &organizations.ListAccountsForParentOutput{}, nil
	}
	page := pages[s.fpIdx]
	s.fpIdx++
	out := &organizations.ListAccountsForParentOutput{Accounts: page}
	if s.fpIdx < len(pages) {
		tok := "next"
		out.NextToken = &tok
	}
	return out, nil
}

func mkAccount(id string, state types.AccountState) types.Account {
	return types.Account{Id: aws.String(id), State: state}
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

func TestListAccounts_ForParent(t *testing.T) {
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

func TestListAccounts_Error(t *testing.T) {
	stub := &stubOrgLister{err: errors.New("unauthorized")}
	_, err := ListAccounts(context.Background(), stub, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
