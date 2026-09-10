package crypto

import "crypto/aes"

// ValidateKey rejects invalid AES-GCM key sizes during service startup.
func ValidateKey(key []byte) error { _, err := aes.NewCipher(key); return err }
