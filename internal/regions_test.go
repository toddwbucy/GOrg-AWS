package internal

import "testing"

func TestAllowedRegions_COM(t *testing.T) {
	regions := AllowedRegions(false)
	want := []string{"us-east-1", "us-east-2", "us-west-1", "us-west-2"}
	if len(regions) != len(want) {
		t.Fatalf("got %d regions, want %d: %v", len(regions), len(want), regions)
	}
	for i, r := range want {
		if regions[i] != r {
			t.Errorf("regions[%d]=%q, want %q", i, regions[i], r)
		}
	}
	// Verify callers cannot mutate the internal slice.
	regions[0] = "mutated"
	if AllowedRegions(false)[0] != "us-east-1" {
		t.Error("AllowedRegions returned a mutable reference to the internal slice")
	}
}

func TestAllowedRegions_GOV(t *testing.T) {
	regions := AllowedRegions(true)
	want := []string{"us-gov-east-1", "us-gov-west-1"}
	if len(regions) != len(want) {
		t.Fatalf("got %d regions, want %d: %v", len(regions), len(want), regions)
	}
	for i, r := range want {
		if regions[i] != r {
			t.Errorf("regions[%d]=%q, want %q", i, regions[i], r)
		}
	}
}

func TestAllowedRegions_NoGovInCOM(t *testing.T) {
	for _, r := range AllowedRegions(false) {
		if len(r) > 6 && r[:7] == "us-gov-" {
			t.Errorf("COM regions should not contain GovCloud region %q", r)
		}
	}
}
