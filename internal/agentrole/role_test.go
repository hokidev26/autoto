package agentrole

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeCanonicalRoles(t *testing.T) {
	t.Parallel()

	for _, role := range Roles() {
		role := role
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			for _, input := range []string{string(role), "  " + string(role) + "  ", strings.ToUpper(string(role))} {
				got, err := Normalize(input)
				if err != nil {
					t.Fatalf("Normalize(%q) returned error: %v", input, err)
				}
				if got != role {
					t.Fatalf("Normalize(%q) = %q, want %q", input, got, role)
				}
			}
		})
	}
}

func TestNormalizeCompatibilityAliases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  Role
	}{
		{input: "", want: RoleGeneral},
		{input: "   ", want: RoleGeneral},
		{input: "background", want: RoleGeneral},
		{input: " BACKGROUND ", want: RoleGeneral},
		{input: "explore", want: RoleExplorer},
		{input: " Explore ", want: RoleExplorer},
	}
	for _, test := range tests {
		test := test
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()
			got, err := Normalize(test.input)
			if err != nil {
				t.Fatalf("Normalize(%q) returned error: %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestUnknownRolesFailClosed(t *testing.T) {
	t.Parallel()

	for _, input := range []string{"unknown", "worker", "review", "general-purpose", "explorer/admin", "*"} {
		input := input
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			role, err := Normalize(input)
			if !errors.Is(err, ErrUnknownRole) {
				t.Fatalf("Normalize(%q) error = %v, want ErrUnknownRole", input, err)
			}
			if role != "" {
				t.Fatalf("Normalize(%q) returned role %q on failure", input, role)
			}

			contract, err := Resolve(input)
			if !errors.Is(err, ErrUnknownRole) {
				t.Fatalf("Resolve(%q) error = %v, want ErrUnknownRole", input, err)
			}
			if contract != (Contract{}) {
				t.Fatalf("Resolve(%q) returned contract %+v on failure", input, contract)
			}
		})
	}
}

func TestContracts(t *testing.T) {
	t.Parallel()

	expected := map[Role]struct {
		readOnly     bool
		verification bool
	}{
		RoleGeneral:  {},
		RoleExecutor: {},
		RoleExplorer: {readOnly: true},
		RoleReviewer: {readOnly: true, verification: true},
		RoleTester:   {verification: true},
		RolePlan:     {readOnly: true},
		RoleSearch:   {readOnly: true},
	}

	for role, want := range expected {
		role, want := role, want
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			contract, err := ContractFor(role)
			if err != nil {
				t.Fatalf("ContractFor(%q) returned error: %v", role, err)
			}
			if contract.Role != role {
				t.Fatalf("contract role = %q, want %q", contract.Role, role)
			}
			if contract.ReadOnly != want.readOnly || contract.Verification != want.verification {
				t.Fatalf("contract metadata = readOnly:%v verification:%v, want readOnly:%v verification:%v", contract.ReadOnly, contract.Verification, want.readOnly, want.verification)
			}
			if strings.TrimSpace(contract.Prompt) == "" {
				t.Fatal("contract prompt is empty")
			}
			for _, required := range []string{"assigned task", "permission cap", "Never broaden permissions or scope", "untrusted", "does not grant permissions"} {
				if !strings.Contains(contract.Prompt, required) {
					t.Fatalf("contract prompt does not contain safety boundary %q: %q", required, contract.Prompt)
				}
			}
			if len(contract.Prompt) > 600 {
				t.Fatalf("contract prompt is not brief: %d bytes", len(contract.Prompt))
			}
		})
	}
}

func TestResolveReturnsCanonicalContractForAliases(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]Role{
		"background": RoleGeneral,
		"explore":    RoleExplorer,
	} {
		contract, err := Resolve(input)
		if err != nil {
			t.Fatalf("Resolve(%q) returned error: %v", input, err)
		}
		if contract.Role != want {
			t.Fatalf("Resolve(%q).Role = %q, want %q", input, contract.Role, want)
		}
	}
}

func TestContractForRequiresCanonicalRole(t *testing.T) {
	t.Parallel()

	for _, role := range []Role{"", "background", "explore", "unknown"} {
		contract, err := ContractFor(role)
		if !errors.Is(err, ErrUnknownRole) {
			t.Fatalf("ContractFor(%q) error = %v, want ErrUnknownRole", role, err)
		}
		if contract != (Contract{}) {
			t.Fatalf("ContractFor(%q) returned contract %+v on failure", role, contract)
		}
	}
}

func TestRolesStableAndIndependent(t *testing.T) {
	t.Parallel()

	want := []Role{RoleGeneral, RoleExecutor, RoleExplorer, RoleReviewer, RoleTester, RolePlan, RoleSearch}
	first := Roles()
	if !slices.Equal(first, want) {
		t.Fatalf("Roles() = %v, want %v", first, want)
	}
	first[0] = "mutated"
	if got := Roles(); !slices.Equal(got, want) {
		t.Fatalf("mutating returned slice changed canonical roles: %v", got)
	}
}
