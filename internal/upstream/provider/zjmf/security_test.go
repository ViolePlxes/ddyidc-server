package zjmf

import "testing"

func TestSecurityGroupsParseScopedIDsWithoutExecutingHTML(t *testing.T) {
	groups, e := parseSecurityGroups(`<table><tr><th>名称</th></tr><tr><td>ddy-42&lt;script&gt;</td><td>描述</td><td><button class="apply" data-id="978">应用</button><button class="trView" data-id="978">查看</button></td></tr><tr><td>broken</td><td></td><td><button class="apply" data-id="not-a-number">应用</button></td></tr></table><script>throw new Error('must never execute')</script>`)
	if e != nil || len(groups) != 1 || groups[0].ID != "978" || groups[0].Name != "ddy-42<script>" || groups[0].Applied {
		t.Fatalf("invalid groups %#v %v", groups, e)
	}
}
