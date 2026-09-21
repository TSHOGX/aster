// Package auth provides password hashing and login rate limiting for the
// orchestrator's credential-based login gate. Hashing uses PBKDF2-HMAC-SHA256
// implemented on the standard library only, so it builds without the
// golang.org/x/crypto module.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"strconv"
	"strings"
)

const (
	// DefaultIterations is the PBKDF2 work factor. 600k matches the OWASP
	// recommendation for PBKDF2-HMAC-SHA256.
	DefaultIterations = 600_000
	saltLen           = 16
	keyLen            = 32
	hashScheme        = "pbkdf2-sha256"
)

// HashPassword derives a PBKDF2-HMAC-SHA256 hash with a fresh random salt and
// returns it in the encoded form "pbkdf2-sha256$<iter>$<salt-b64>$<hash-b64>".
func HashPassword(password string) (string, error) {
	return HashPasswordWithIterations(password, DefaultIterations)
}

// HashPasswordWithIterations is HashPassword with an explicit work factor. It
// exists mainly so tests can use a cheaper factor.
func HashPasswordWithIterations(password string, iterations int) (string, error) {
	if iterations <= 0 {
		return "", fmt.Errorf("iterations must be > 0")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	hash := pbkdf2SHA256([]byte(password), salt, iterations, keyLen)
	return encodeHash(iterations, salt, hash), nil
}

// VerifyPassword reports whether password matches the encoded PBKDF2 hash. The
// comparison is constant-time. A malformed encoding returns false.
func VerifyPassword(encoded, password string) bool {
	iterations, salt, want, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

func encodeHash(iterations int, salt, hash []byte) string {
	return strings.Join([]string{
		hashScheme,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	}, "$")
}

func decodeHash(encoded string) (iterations int, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return 0, nil, nil, fmt.Errorf("invalid hash encoding")
	}
	iterations, err = strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return 0, nil, nil, fmt.Errorf("invalid iterations")
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return 0, nil, nil, fmt.Errorf("invalid salt")
	}
	hash, err = base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(hash) == 0 {
		return 0, nil, nil, fmt.Errorf("invalid hash")
	}
	return iterations, salt, hash, nil
}

// pbkdf2SHA256 implements PBKDF2 (RFC 8018) with HMAC-SHA256 as the PRF. This is
// a direct, standard implementation kept local to avoid an x/crypto dependency.
func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	prf := hmac.New(sha256.New, password)
	hashLen := prf.Size()
	numBlocks := (keyLength + hashLen - 1) / hashLen

	out := make([]byte, 0, numBlocks*hashLen)
	block := make([]byte, 4)
	for blockIndex := 1; blockIndex <= numBlocks; blockIndex++ {
		prf.Reset()
		prf.Write(salt)
		binary.BigEndian.PutUint32(block, uint32(blockIndex))
		prf.Write(block)
		u := prf.Sum(nil)

		t := make([]byte, len(u))
		copy(t, u)
		for n := 2; n <= iterations; n++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(u[:0])
			for i := range t {
				t[i] ^= u[i]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLength]
}

// ValidPasswordHash validates operator-supplied encodings before enabling login.
func ValidPasswordHash(encoded string) bool {
	iterations, salt, key, err := decodeHash(encoded)
	return err == nil && iterations <= 10000000 && len(salt) >= 16 && len(key) == 32
}
