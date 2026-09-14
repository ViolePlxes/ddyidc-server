package navigation

import (
	"encoding/json"
	"testing"
)

func TestHrefPolicy(t *testing.T) {
	for _, v := range []string{"/console/orders", "/console/orders?status=1", "https://example.com/path", "http://example.com"} {
		if !validHref(v) {
			t.Errorf("valid URL rejected: %q", v)
		}
	}
	for _, v := range []string{"", "//example.com", "/\\example.com", "javascript:alert(1)", "data:text/html,test", "https://user:pass@example.com", "/path\n", "/space here"} {
		if validHref(v) {
			t.Errorf("unsafe URL accepted: %q", v)
		}
	}
}
func TestFullOrder(t *testing.T) {
	nodes := []Node{{ID: "1"}, {ID: "2"}}
	if !fullOrder(nodes, []string{"2", "1"}) {
		t.Fatal("valid reorder rejected")
	}
	for _, ids := range [][]string{{"1"}, {"1", "1"}, {"1", "3"}, {"1", "2", "3"}} {
		if fullOrder(nodes, ids) {
			t.Fatalf("invalid order accepted: %v", ids)
		}
	}
}
func TestSeeds(t *testing.T) {
	var nodes []Node
	if err := json.Unmarshal(seed, &nodes); err != nil {
		t.Fatal(err)
	}
	byID := map[string]Node{}
	for _, n := range nodes {
		if _, ok := byID[n.ID]; ok {
			t.Fatal("duplicate ID")
		}
		byID[n.ID] = n
	}
	count := map[string]int{}
	for _, n := range nodes {
		count[n.Scope]++
		if !validScope(n.Scope) {
			t.Fatal("invalid scope")
		}
		if n.ParentID != "0" {
			p, ok := byID[n.ParentID]
			if !ok || p.ParentID != "0" || p.Scope != n.Scope {
				t.Fatal("invalid parent")
			}
			if !validHref(n.Href) {
				t.Fatal("invalid seed href")
			}
		}
	}
	if count["console"] != 30 || count["admin"] != 50 || count["sales"] != 3 {
		t.Fatalf("unexpected seed inventory: %v", count)
	}
}
