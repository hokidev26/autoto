//go:build windows

package secrets

import (
	"bytes"
	"unsafe"

	"golang.org/x/sys/windows"
)

// protectKeyMaterial wraps raw key bytes with the per-user DPAPI secret so the
// key file is unreadable to other accounts and to offline copies of the
// profile. If DPAPI is unavailable the raw key is stored, matching the
// non-Windows key file format.
func protectKeyMaterial(key []byte) []byte {
	if len(key) == 0 {
		return nil
	}
	in := windows.DataBlob{Size: uint32(len(key)), Data: &key[0]}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return append([]byte(nil), key...)
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	wrapped := make([]byte, 0, len(credentialKeyDPAPIPrefix)+int(out.Size))
	wrapped = append(wrapped, credentialKeyDPAPIPrefix...)
	wrapped = append(wrapped, unsafe.Slice(out.Data, out.Size)...)
	return wrapped
}

// unprotectKeyMaterial recovers raw key bytes from a key file. Raw key files
// (created by non-Windows builds sharing the profile, or by a DPAPI fallback)
// pass through unchanged.
func unprotectKeyMaterial(wrapped []byte) ([]byte, error) {
	if !bytes.HasPrefix(wrapped, credentialKeyDPAPIPrefix) {
		return append([]byte(nil), wrapped...), nil
	}
	blob := wrapped[len(credentialKeyDPAPIPrefix):]
	if len(blob) == 0 {
		return nil, ErrCredentialKeyUnavailable
	}
	in := windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, ErrCredentialKeyUnavailable
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
