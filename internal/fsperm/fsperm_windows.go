//go:build windows

package fsperm

import (
	"fmt"
	"strings"

	"golang.org/x/sys/windows"
)

// sddl builds a protected DACL (no inherited entries) granting full control
// to SYSTEM, Administrators and the current user only.
func sddl(inherit bool) (string, error) {
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	sid := u.User.Sid.String()
	return fmt.Sprintf("D:P(A;%[1]s;FA;;;SY)(A;%[1]s;FA;;;BA)(A;%[1]s;FA;;;%[2]s)", flags, sid), nil
}

func apply(path string, inherit bool) error {
	s, err := sddl(inherit)
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(s)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

func restrictDir(dir string) error { return apply(dir, true) }

func restrictFile(path string) error { return apply(path, false) }

// private checks that the DACL grants access to no one but SYSTEM,
// Administrators, the owner/current user and CREATOR OWNER.
func private(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	tok := windows.GetCurrentProcessToken()
	u, err := tok.GetTokenUser()
	if err != nil {
		return false, err
	}
	allowed := map[string]bool{"SY": true, "BA": true, "CO": true, u.User.Sid.String(): true}
	s := sd.String()
	i := strings.Index(s, "D:")
	if i < 0 {
		return false, nil
	}
	for _, ace := range strings.Split(s[i:], "(")[1:] {
		f := strings.Split(strings.TrimSuffix(strings.SplitN(ace, ")", 2)[0], ")"), ";")
		if len(f) < 6 {
			return false, nil
		}
		if f[0] == "A" && !allowed[f[5]] {
			return false, nil
		}
	}
	return true, nil
}
