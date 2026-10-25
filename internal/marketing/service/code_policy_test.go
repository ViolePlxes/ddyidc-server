package marketing

import "testing"

func TestCodePolicy(t *testing.T) {
	p := CodePolicy{BlockedTerms: []string{"BADWORD"}, ReservedCodes: []string{"ZX9K"}}
	for _, v := range []string{"Ab9Z", "CODE7X2Q", "Z9K2D7F4A8Q3"} {
		if issue := codeIssue(v, p); issue != "" {
			t.Errorf("valid %q: %s", v, issue)
		}
	}
	for _, v := range []string{"", "abc", "ABCDEFGHIJKLM", "汉字优惠码", "AB_C", "A B9Z", "888888", "666666", "6688", "888666", "123456", "654321", "7890", "AAAA", "ABAB", "ABCD", "ADMIN123", "DDYIDC", "FANDONG", "F4NDONG", "FAND0NG", "FANNDONG", "ZHADAN", "NAZI123", "BADWORD9", "ZX9K"} {
		if codeIssue(v, p) == "" {
			t.Errorf("unsafe/reserved code accepted %q", v)
		}
	}
	if normalizedCode(" ab9z ") != "AB9Z" {
		t.Fatal("case canonicalization")
	}
}
