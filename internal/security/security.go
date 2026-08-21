package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // #nosec G505 -- RFC 6238 permits HMAC-SHA-1 for TOTP.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	passwordTime    uint32 = 3
	passwordMemory  uint32 = 64 * 1024
	passwordThreads uint8  = 4
	passwordKeySize uint32 = 32
	totpPeriod             = 30 * time.Second
)

// RandomToken returns an opaque token with at least bytesLength bytes of entropy.
func RandomToken(bytesLength int) (string, error) {
	b := make([]byte, bytesLength)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("read random token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// TokenHash is safe to persist for high-entropy opaque tokens.
func TokenHash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

func HashPassword(password string) (string, error) {
	if len(password) < 12 {
		return "", errors.New("password must contain at least 12 characters")
	}
	return hashPassword(password)
}

// HashTemporaryPassword is reserved for a bootstrap credential that must be
// replaced before the authenticated session can access any management API.
func HashTemporaryPassword(password string) (string, error) {
	if password == "" {
		return "", errors.New("temporary password is empty")
	}
	return hashPassword(password)
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("read password salt: %w", err)
	}
	hash := argon2.IDKey([]byte(password), salt, passwordTime, passwordMemory, passwordThreads, passwordKeySize)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", passwordMemory, passwordTime, passwordThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false, errors.New("invalid stored password hash")
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return false, errors.New("invalid password hash parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("invalid password hash salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false, errors.New("invalid password hash value")
	}
	actual := argon2.IDKey([]byte(password), salt, iterations, memory, threads, uint32(len(expected)))
	return subtle.ConstantTimeCompare(expected, actual) == 1, nil
}

func NewTOTPSecret() (string, error) {
	b := make([]byte, 20)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", fmt.Errorf("read TOTP secret: %w", err)
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), nil
}

// TOTPCounterAt returns the RFC 6238 counter for at shifted by offset
// time-steps. Two submissions of the same code share a counter, so callers
// use it to demand a fresh code during clock resynchronization.
func TOTPCounterAt(at time.Time, offset int64) int64 {
	return at.Unix()/int64(totpPeriod.Seconds()) + offset
}

// VerifyTOTPAround accepts codes within radius time-steps of center (itself in
// time-steps relative to at) and returns the step the code matched. The
// authenticator's clock and the server's routinely disagree by more than one
// step — VPS and WSL hosts drift by minutes — so enrollment searches a wide
// radius to learn the skew, and login re-centers on the stored value instead
// of trusting the server clock alone.
func VerifyTOTPAround(secret, code string, at time.Time, center, radius int64) (int64, bool) {
	code = strings.Join(strings.Fields(code), "")
	if len(code) != 6 {
		return 0, false
	}
	for offset := center - radius; offset <= center+radius; offset++ {
		if subtle.ConstantTimeCompare([]byte(totpCode(secret, at.Add(time.Duration(offset)*totpPeriod))), []byte(code)) == 1 {
			return offset, true
		}
	}
	return 0, false
}

func totpCode(secret string, at time.Time) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return ""
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(at.Unix()/int64(totpPeriod.Seconds())))
	mac := hmac.New(sha1.New, key) // #nosec G401 -- required for RFC 6238 compatibility.
	_, _ = mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}

// Encrypt uses AES-256-GCM and prefixes the ciphertext with a random nonce.
func Encrypt(key, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func Decrypt(key, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext is too short")
	}
	return gcm.Open(nil, ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():], nil)
}

func ParsePositiveInt(raw string, fallback, maximum int) (int, error) {
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maximum {
		return 0, fmt.Errorf("must be between 1 and %d", maximum)
	}
	return value, nil
}
