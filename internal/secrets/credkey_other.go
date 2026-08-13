//go:build !windows

package secrets

import "bytes"

// protectKeyMaterial stores the raw key bytes; non-Windows platforms rely on
// the 0o600 key file plus directory permissions.
func protectKeyMaterial(key []byte) []byte {
	return append([]byte(nil), key...)
}

// unprotectKeyMaterial recovers raw key bytes from a key file. DPAPI-wrapped
// keys can only be recovered on the Windows account that created them.
func unprotectKeyMaterial(wrapped []byte) ([]byte, error) {
	if bytes.HasPrefix(wrapped, credentialKeyDPAPIPrefix) {
		return nil, ErrCredentialKeyUnavailable
	}
	return append([]byte(nil), wrapped...), nil
}
