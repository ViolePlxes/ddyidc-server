package db

import (
	"fmt"
	"os"
)

// ApplySQLFile executes the complete schema script. It is deliberately opt-in:
// the supplied development script contains DROP TABLE statements and must never
// be run on every normal service start.
func ApplySQLFile(path string) error {
	if G == nil {
		return fmt.Errorf("database is not initialized")
	}
	script, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read migration file: %w", err)
	}
	if err := G.Exec(string(script)).Error; err != nil {
		return fmt.Errorf("execute migration file: %w", err)
	}
	return nil
}
