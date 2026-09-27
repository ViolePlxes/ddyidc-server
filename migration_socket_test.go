package main

import "testing"

func TestOfflineMigrationSocketBoundary(t *testing.T) {
	for _, tc := range []struct {
		name         string
		offline      bool
		uid          int
		socket, host string
		allowed      bool
	}{
		{"local root", true, 0, "/var/run/mysqld/mysqld.sock", "127.0.0.1", true},
		{"IPv6 loopback", true, 0, "/var/run/mysqld/mysqld.sock", "::1", true},
		{"normal API", false, 0, "/var/run/mysqld/mysqld.sock", "127.0.0.1", false},
		{"service identity", true, 1000, "/var/run/mysqld/mysqld.sock", "127.0.0.1", false},
		{"different socket", true, 0, "/tmp/other.sock", "127.0.0.1", false},
		{"remote database", true, 0, "/var/run/mysqld/mysqld.sock", "db.example.com", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateMigrationSocket(tc.offline, tc.uid, tc.socket, tc.host) == nil; got != tc.allowed {
				t.Fatalf("allowed=%v want=%v", got, tc.allowed)
			}
		})
	}
}
