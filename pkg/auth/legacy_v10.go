package auth

import (
	"crypto/md5" // Required only for verifying imported v10 credentials.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// The prefix is assigned only by the controlled v10 importer. Normal account
// creation and administrator authentication never accept the legacy algorithm.
const V10PasswordPrefix = "v10:"
const prehashedBcryptPrefix = "bcrypt-sha256:"

var v10HashPattern = regexp.MustCompile(`^v10:###[0-9a-f]{32}$`)

func IsV10Password(hash string) bool { return v10HashPattern.MatchString(hash) }

// VerifyV10Password implements app/common.php:idcsmart_password exactly for
// non-ZKEYS v10 installations. authCode must come from the original installation.
func VerifyV10Password(hash, password, authCode string) bool {
	if authCode == "" || !IsV10Password(hash) {
		return false
	}
	first := md5.Sum([]byte(authCode + password))
	second := md5.Sum([]byte(hex.EncodeToString(first[:])))
	expected := V10PasswordPrefix + "###" + hex.EncodeToString(second[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(hash)) == 1
}

// LoadV10AuthCode reads a separate, owner-only secret file. It never guesses a
// key, prints it, or falls back to an empty salt. Only one trailing line ending
// is removed; whitespace in the actual key is preserved.
func LoadV10AuthCode() (string, error) {
	path := os.Getenv("DDYIDC_V10_AUTHCODE_FILE")
	if path == "" {
		return "", errors.New("旧密码校验配置未就绪")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", errors.New("旧密码校验配置不可读")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return "", errors.New("旧密码校验配置权限或格式无效")
	}
	data := make([]byte, info.Size())
	if _, err := file.ReadAt(data, 0); err != nil {
		return "", errors.New("旧密码校验配置不可读")
	}
	key := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
	if key == "" {
		return "", errors.New("旧密码校验配置为空")
	}
	return key, nil
}

// HashMigratedPassword preserves passwords longer than bcrypt's 72-byte limit
// with an explicitly versioned SHA-256 prehash. Ordinary passwords use the
// platform's existing bcrypt policy unchanged. No password bytes are truncated.
func HashMigratedPassword(password string) (string, error) {
	if len([]byte(password)) <= 72 {
		return HashPassword(password)
	}
	sum := sha256.Sum256([]byte(password))
	hash, err := bcrypt.GenerateFromPassword([]byte(hex.EncodeToString(sum[:])), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return prehashedBcryptPrefix + string(hash), nil
}

func matchesPrehashedBcrypt(hash, password string) bool {
	sum := sha256.Sum256([]byte(password))
	return bcrypt.CompareHashAndPassword([]byte(strings.TrimPrefix(hash, prehashedBcryptPrefix)), []byte(hex.EncodeToString(sum[:]))) == nil
}
