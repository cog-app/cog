package update

import (
	"crypto/ed25519"
	"encoding/base64"
)

// TrustedKeys are the public keys a release list may be signed with. The
// private half of the first is the COG_RELEASE_KEY secret of the GitHub
// repository's release workflow (go tool release sign). To rotate, add the
// new key here, ship a release signed with the old one, then sign with
// the new one and remove the old one in a later release.
var TrustedKeys = []ed25519.PublicKey{
	mustKey("2wfZh8T1D705D73jSwc4sUWvZ6pQQacqWXOVf8ZWbVQ="),
}

func mustKey(b64 string) ed25519.PublicKey {
	k, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(k) != ed25519.PublicKeySize {
		panic("update: bad trusted key " + b64)
	}
	return k
}
