//go:build windows

package server

import (
	"os"

	"golang.org/x/sys/windows"
)

// hardenTokenPermissions rewrites the DACL of the secrets path so only the
// current user can access it. Files under the profile otherwise inherit the
// broad profile ACL, which is the Windows equivalent of the 0700/0600 modes
// applied on other platforms. PROTECTED_DACL detaches inherited ACEs so the
// explicit owner-only grant is the entire ACL.
func hardenTokenPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if info.IsDir() {
		// Future files created inside the secrets dir inherit the owner-only
		// grant even if a writer forgets to harden them individually.
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	dacl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	)
}
