package files

import (
	"crypto/sha256"
	"encoding/hex"
)

// HashLen is the number of hex characters in a Hash. A caller echoes a Hash
// back on Edit.ExpectHash, so HashLen is also the width of that precondition.
const HashLen = 12

// Hash fingerprints file content for staleness detection.
//
// It is a truncated sha256: 12 hex characters, 48 bits. The truncation is
// deliberate. A model has to carry this value from a read to a later edit, and
// a full 64-character digest costs tokens on every hop to answer a question
// that only needs "is this the same content I was shown".
//
// 48 bits is not a security boundary and is not trying to be. It guards
// against an accidental change. A formatter ran, a sibling tool wrote the
// file, or the user saved in their editor. Anyone positioned to search for a
// collision already has write access to the file and can simply write what
// they want, so a wider digest would add nothing.
func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])[:HashLen]
}
