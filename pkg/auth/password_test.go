package auth

import "testing"

func TestPasswordHashRoundTrip(t *testing.T) {
	const fixturePassword = "fixture-secret-9"
	hash, err := HashPassword(fixturePassword)
	if err != nil {
		t.Fatal(err)
	}
	if hash == fixturePassword || !PasswordMatches(hash, fixturePassword) {
		t.Fatal("password hash did not round trip")
	}
	if PasswordMatches(hash, "wrong-password") {
		t.Fatal("wrong password matched")
	}
}
