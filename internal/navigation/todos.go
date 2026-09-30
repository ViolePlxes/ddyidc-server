package navigation

import (
	"context"
	"ddyidc/internal/admintodo"
	"fmt"
)

func todoNode(n Node) bool { return admintodo.Path(n.Gate) != "" || admintodo.Path(n.Href) != "" }

func filterTodos(nodes []Node, super bool) []Node {
	if super {
		return nodes
	}
	blocked := map[string]bool{}
	for _, n := range nodes {
		if todoNode(n) {
			blocked[n.ID] = true
		}
	}
	out := make([]Node, 0, len(nodes))
	for _, n := range nodes {
		if !blocked[n.ID] && !blocked[n.ParentID] {
			out = append(out, n)
		}
	}
	return out
}

func (s *Service) decorateTodos(ctx context.Context, out Snapshot, super bool, counts bool) Snapshot {
	out.Nodes = filterTodos(out.Nodes, super)
	if !super || !counts {
		return out
	}
	needed := false
	for _, n := range out.Nodes {
		if todoNode(n) {
			needed = true
			break
		}
	}
	if !needed {
		return out
	}
	summary := admintodo.Read(ctx, s.db)
	badges := map[string]string{admintodo.Root: fmt.Sprint(summary.Total)}
	if !summary.Complete {
		badges[admintodo.Root] = "—"
	}
	for _, item := range summary.Items {
		value := "—"
		if item.Count != nil {
			value = fmt.Sprint(*item.Count)
		}
		badges[admintodo.Root+"/"+item.Key] = value
	}
	for i, n := range out.Nodes {
		path := admintodo.Path(n.Gate)
		if path == "" {
			path = admintodo.Path(n.Href)
		}
		if value, ok := badges[path]; ok {
			out.Nodes[i].Badge = &value
		}
	}
	return out
}
