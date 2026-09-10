package update

import "testing"

func TestToolVersionArguments(t *testing.T) {
	goArgs := toolVersionArgs(TargetBackend)
	if len(goArgs) != 1 || goArgs[0] != "version" {
		t.Fatalf("Go version arguments are invalid: %v", goArgs)
	}
	npmArgs := toolVersionArgs(TargetFrontend)
	if len(npmArgs) != 1 || npmArgs[0] != "--version" {
		t.Fatalf("npm version arguments are invalid: %v", npmArgs)
	}
}
