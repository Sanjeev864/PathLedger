//go:build windows

package fsperm

import (
	"fmt"
	"unsafe"

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

// private checks that every allow entry in the DACL, inherited or not, is
// for SYSTEM, Administrators, CREATOR OWNER or the current user.
func private(path string) (bool, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false, err
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false, err
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return false, err
	}
	var allowed []*windows.SID
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid, windows.WinCreatorOwnerSid} {
		sid, err := windows.CreateWellKnownSid(t)
		if err != nil {
			return false, err
		}
		allowed = append(allowed, sid)
	}
	allowed = append(allowed, u.User.Sid)
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return false, err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		ok := false
		for _, a := range allowed {
			if sid.Equals(a) {
				ok = true
			}
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}
