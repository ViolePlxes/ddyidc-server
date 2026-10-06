package zjmf

import "testing"

func TestSecurityGroupsParseScopedIDsWithoutExecutingHTML(t *testing.T) {
	groups, e := parseSecurityGroups(`<table><tr><th>名称</th></tr><tr><td>ddy-42&lt;script&gt;</td><td>描述</td><td><button class="apply" data-id="978">应用</button><button class="trView" data-id="978">查看</button></td></tr><tr><td>broken</td><td></td><td><button class="apply" data-id="not-a-number">应用</button></td></tr></table><script>throw new Error('must never execute')</script>`)
	if e != nil || len(groups) != 1 || groups[0].ID != "978" || groups[0].Name != "ddy-42<script>" || groups[0].Applied {
		t.Fatalf("invalid groups %#v %v", groups, e)
	}
}

func TestSecurityGroupsAppliedNameKeepsOwnershipMarker(t *testing.T) {
	for _, prefix := range []string{"(当前)", "（当前）"} {
		groups, err := parseSecurityGroups(`<table><tr><td>` + prefix + ` ddy-901697731749</td><td>专属测试组</td><td><button class="trView" data-id="1504">查看</button></td></tr></table>`)
		if err != nil || len(groups) != 1 || groups[0].ID != "1504" || groups[0].Name != "ddy-901697731749" || !groups[0].Applied {
			t.Fatalf("bound group ownership marker lost: %#v %v", groups, err)
		}
	}
}
