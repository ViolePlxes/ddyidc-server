package navigation

import (
	"context"
	"ddyidc/internal/shared/testenv"
	"testing"
)

func TestTodoVisibility(t *testing.T) {
	nodes := []Node{{ID: "1", Gate: "/admin/todos"}, {ID: "2", ParentID: "1", Href: "/renamed"}, {ID: "3", Href: "https://example.test/admin/%74odos/tickets"}, {ID: "4", Href: "/admin/orders"}}
	hidden := filterTodos(nodes, false)
	if len(hidden) != 1 || hidden[0].ID != "4" {
		t.Fatal("ordinary role can see todo navigation")
	}
	if len(filterTodos(nodes, true)) != 4 {
		t.Fatal("super admin navigation lost")
	}
}

func TestTodoNavigationConfigurationMySQL(t *testing.T) {
	db := testenv.MySQL(t)
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	s := New(tx)
	ctx := context.Background()
	before, err := s.List(ctx, "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	var root Node
	children := 0
	for _, n := range before.Nodes {
		if n.Gate == "/admin/todos" {
			root = n
		}
		if n.ParentID == "930000001" {
			children++
		}
	}
	if root.ID == "" || children != 9 {
		t.Fatalf("missing persisted todo tree: root=%+v children=%d nodes=%d", root, children, len(before.Nodes))
	}
	enabled := false
	if err = s.Change(ctx, "admin", root.ID, "save", Input{Revision: &before.Revision, ParentID: "0", Name: "自定义待办", Icon: root.Icon, Enabled: &enabled}); err != nil {
		t.Fatal(err)
	}
	after, err := s.List(ctx, "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range after.Nodes {
		if n.ID == root.ID && (n.Name != "自定义待办" || n.Enabled || n.Gate != "/admin/todos") {
			t.Fatal("editor change or stable badge binding lost")
		}
	}
	if after.Revision != before.Revision+1 {
		t.Fatal("revision not advanced")
	}
	if err = s.Change(ctx, "admin", root.ID, "delete", Input{Revision: &after.Revision}); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.List(ctx, "admin", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range deleted.Nodes {
		if todoNode(n) {
			t.Fatal("deleted todo tree persisted")
		}
	}
}
