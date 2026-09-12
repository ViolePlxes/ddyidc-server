package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword applies the password hashing policy shared by customer account
// creation flows. Passwords are never logged or returned by this package.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// PasswordMatches checks a plaintext password against the stored bcrypt hash.
func PasswordMatches(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}
