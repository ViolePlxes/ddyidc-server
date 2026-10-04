package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Explicitly reviewed long-lived launchers. This is not a baseline of arbitrary
// goroutines: each launcher delegates work to a durable lease or leader barrier.
var launchers = map[string]string{
	"internal/payment/service/durable.go:Start":               "tasks.Run",
	"internal/order/service/stock_expiry.go:StartStockExpiry": "distsync.Work",
	"internal/groupbuy/service.go:StartMaintenance":           "distsync.Task",
	"internal/marketplace/trade.go:StartMaintenance":          "distsync.Task",
}

var directSQL = regexp.MustCompile(`(?i)\b(update|insert\s+into|replace\s+into|delete\s+from)\s+billing\s*\.\s*(accounts|transactions)\b`)

func inspectSource(path string, source []byte) ([]string, error) {
	billing, err := inspectOwnedSource(path, source, "billing")
	if err != nil {
		return nil, err
	}
	payments, err := inspectOwnedSource(path, source, "payment")
	result := append(billing, payments...)
	if err != nil {
		return result, err
	}
	orders, e := inspectOwnedSource(path, source, "order")
	result = append(result, orders...)
	if e != nil {
		return result, e
	}
	receipts, e := inspectOwnedSource(path, source, "receipt-append")
	return append(result, receipts...), e
}

func inspectOwnedSource(path string, source []byte, owner string) ([]string, error) {
	f, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
	if err != nil {
		return nil, err
	}
	var violations []string
	// Fold literal concatenations, so split SQL constants are not an escape hatch.
	var literal func(ast.Expr) string
	literal = func(e ast.Expr) string {
		switch x := e.(type) {
		case *ast.BasicLit:
			if x.Kind == token.STRING {
				s, _ := strconv.Unquote(x.Value)
				return s
			}
		case *ast.BinaryExpr:
			if x.Op == token.ADD {
				return literal(x.X) + literal(x.Y)
			}
		}
		return ""
	}
	engine := path == "internal/billing/balance/balance.go"
	writeSQL := directSQL
	modelImport := "ddyidc/internal/billing/balance"
	models := map[string]bool{"Account": true, "Transaction": true}
	tables := map[string]bool{"billing.accounts": true, "billing.transactions": true}
	ownerLabel := "BalanceService.Change"
	if owner == "payment" {
		engine = strings.HasPrefix(path, "internal/payment/repository/")
		writeSQL = regexp.MustCompile(`(?i)\b(update|insert\s+into|replace\s+into|delete\s+from)\s+payment\s*\.\s*(payments|original_refunds|receipts|receipt_reviews)\b`)
		modelImport = "ddyidc/internal/payment/domain"
		models = map[string]bool{"Payment": true, "Refund": true}
		tables = map[string]bool{"payment.payments": true, "payment.original_refunds": true, "payment.receipts": true, "payment.receipt_reviews": true}
		ownerLabel = "payment repository"
	}
	if owner == "order" {
		engine = strings.HasPrefix(path, "internal/order/")
		writeSQL = regexp.MustCompile(`(?i)\b(update|replace\s+into|delete\s+from)\s+order\s*\.\s*orders\b`)
		modelImport = "ddyidc/internal/order/domain"
		models = map[string]bool{"Order": true}
		tables = map[string]bool{"order.orders": true}
		ownerLabel = "order transaction API"
	}
	if owner == "receipt-append" {
		// Even the owning repository may only append captured receipt facts.
		engine = false
		writeSQL = regexp.MustCompile(`(?i)\b(update|replace\s+into|delete\s+from)\s+payment\s*\.\s*receipts\b`)
		modelImport = "ddyidc/internal/payment/domain"
		models = map[string]bool{}
		tables = map[string]bool{"payment.receipts": true}
		ownerLabel = "append-only receipt contract"
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && !engine && writeSQL.MatchString(strings.ReplaceAll(literal(e), "`", "")) {
			violations = append(violations, "direct SQL write outside "+ownerLabel)
		}
		return true
	})
	if !engine {
		balanceAliases := map[string]bool{}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == modelImport {
				name := "billing"
				if owner == "payment" {
					name = "payment"
				}
				if owner == "order" {
					name = "order"
				}
				if imp.Name != nil {
					name = imp.Name.Name
				}
				if name == "." {
					violations = append(violations, "dot import obscures protected models: "+modelImport)
				}
				balanceAliases[name] = true
			}
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			tracked := map[string]bool{}
			var protected func(ast.Node) bool
			protected = func(n ast.Node) bool {
				hit := false
				ast.Inspect(n, func(n ast.Node) bool {
					switch x := n.(type) {
					case *ast.Ident:
						hit = hit || tracked[x.Name]
					case *ast.SelectorExpr:
						if id, ok := x.X.(*ast.Ident); ok && balanceAliases[id.Name] && models[x.Sel.Name] {
							hit = true
						}
					case *ast.CallExpr:
						if sel, ok := x.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Table" && len(x.Args) > 0 {
							v := strings.ToLower(strings.ReplaceAll(literal(x.Args[0]), "`", ""))
							parts := strings.Fields(v)
							hit = hit || (len(parts) > 0 && tables[parts[0]])
						}
					}
					return !hit
				})
				return hit
			}
			// Propagate local model/query aliases to a fixed point, not just fluent chains.
			for changed := true; changed; {
				changed = false
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					mark := func(id *ast.Ident) {
						if !tracked[id.Name] {
							tracked[id.Name] = true
							changed = true
						}
					}
					switch x := n.(type) {
					case *ast.ValueSpec:
						if x.Type != nil && protected(x.Type) {
							for _, id := range x.Names {
								mark(id)
							}
						}
						for i, value := range x.Values {
							if i < len(x.Names) && protected(value) {
								mark(x.Names[i])
							}
						}
					case *ast.AssignStmt:
						for i, rhs := range x.Rhs {
							if i < len(x.Lhs) && protected(rhs) {
								if id, ok := x.Lhs[i].(*ast.Ident); ok {
									mark(id)
								}
							}
						}
					}
					return true
				})
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch sel.Sel.Name {
				case "Create", "Save", "Update", "Updates", "UpdateColumn", "UpdateColumns", "Delete":
					if (owner == "order" || owner == "receipt-append") && sel.Sel.Name == "Create" {
						return true
					}
					hit := protected(sel.X)
					for _, arg := range call.Args {
						hit = hit || protected(arg)
					}
					if hit {
						violations = append(violations, "ORM write outside "+ownerLabel+": "+fn.Name.Name)
					}
				}
				return true
			})
		}
	}
	if owner != "billing" {
		return violations, nil
	}
	critical := false
	for _, p := range []string{"internal/payment/", "internal/order/", "internal/groupbuy/", "internal/marketplace/", "internal/instance/service/"} {
		critical = critical || strings.HasPrefix(path, p)
	}
	if !critical {
		return violations, nil
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		count := 0
		delegated := false
		expected := launchers[path+":"+fn.Name.Name]
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if launch, ok := n.(*ast.GoStmt); ok {
				count++
				// Inspect the launched call/closure, not unrelated synchronous
				// code elsewhere in the registered function.
				ast.Inspect(launch.Call, func(inner ast.Node) bool {
					if call, ok := inner.(*ast.CallExpr); ok {
						if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
							if id, ok := sel.X.(*ast.Ident); ok && id.Name+"."+sel.Sel.Name == expected {
								delegated = true
							}
						}
					}
					return true
				})
			}
			return true
		})
		if count > 0 && (expected == "" || count != 1 || !delegated) {
			violations = append(violations, "unregistered critical goroutine: "+fn.Name.Name)
		}
	}
	return violations, nil
}

func TestArchitectureBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Offline schema/migrations contain reviewed conversion SQL, never handlers.
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "internal/shared/migrations/") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		found, err := inspectSource(rel, source)
		if err != nil {
			return err
		}
		for _, v := range found {
			t.Errorf("%s: %s", rel, v)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGuardMutationFixtures(t *testing.T) {
	for _, body := range []string{
		"func handle(){go deliver()}",
		"func handle(){db.Exec(\"UPDATE `billing`.`accounts` SET balance=1\")}",
		"func handle(){db.Exec(\"DELETE FROM billing.\"+\"transactions WHERE id=1\")}",
		"func handle(){q:=db.Table(\"billing.accounts\"); q.Update(\"balance\",1)}",
		"func handle(){var q = db.Table(\"billing.accounts\"); var alias = q; alias.Update(\"balance\",1)}",
		"func handle(){var q = db.Table(\"payment.payments AS p\"); q.Update(\"status\",\"paid\")}",
		"func handle(){db.Exec(\"UPDATE payment.original_refunds SET status='refunded'\")}",
		"func handle(){db.Exec(\"INSERT INTO payment.\"+\"receipts (id) VALUES (1)\")}",
		"func handle(){db.Exec(\"DELETE FROM `payment`.`receipt_reviews` WHERE payment_id=1\")}",
		"func handle(){q:=db.Table(\"payment.receipts AS r\"); alias:=q; alias.Create(fields)}",
		"func handle(){var q=db.Table(\"payment.receipt_reviews\"); q.Updates(fields)}",
		"import pay \"ddyidc/internal/payment/domain\"; func handle(){var p pay.Payment; q:=db.Model(&p); q.Save(&p)}",
		"import \"ddyidc/internal/payment/domain\"; func handle(){var p payment.Payment; db.Save(&p)}",
		"import \"ddyidc/internal/billing/balance\"; func handle(){var p billing.Account; db.Save(&p)}",
	} {
		found, err := inspectSource("internal/order/service/new.go", []byte("package order\n"+body))
		if err != nil || len(found) == 0 {
			t.Fatal("unsafe mutation not rejected", err)
		}
	}
	found, err := inspectSource("internal/order/service/read.go", []byte("package order\nfunc read(){db.Raw(\"SELECT balance FROM billing.accounts\")}\n// go deliver()"))
	if err != nil || len(found) != 0 {
		t.Fatal("read-only code rejected", err)
	}
	found, err = inspectSource("internal/payment/service/durable.go", []byte("package payment\nfunc NewDurableDispatcher(){go chargeAgain(); tasks.Run(ctx,db,handler)}"))
	if err != nil || len(found) == 0 {
		t.Fatal("unrelated delegation hid unsafe goroutine", err)
	}
}

func TestReceiptFactsAppendOnly(t *testing.T) {
	for _, body := range []string{
		`func change(){db.Exec("UPDATE payment.receipts SET amount=0")}`,
		`func change(){db.Exec("DELETE FROM payment.receipts WHERE id=1")}`,
		`func change(){db.Exec("REPLACE INTO payment.receipts (id) VALUES (1)")}`,
		`func change(){q:=db.Table("payment.receipts AS r"); alias:=q; alias.Updates(fields)}`,
		`func change(){db.Table("payment.receipts").Save(fields)}`,
	} {
		found, err := inspectSource("internal/payment/repository/receipts.go", []byte("package repository\n"+body))
		if err != nil || len(found) == 0 {
			t.Fatalf("receipt fact mutation accepted: %v", err)
		}
	}
	for _, body := range []string{
		`func appendFact(){db.Table("payment.receipts").Create(fields)}`,
		`func read(){db.Table("payment.receipts").Find(&rows)}`,
		`func resolve(){db.Table("payment.receipt_reviews").Updates(fields)}`,
	} {
		found, err := inspectSource("internal/payment/repository/receipts.go", []byte("package repository\n"+body))
		if err != nil || len(found) != 0 {
			t.Fatalf("legitimate receipt operation rejected: %v %v", err, found)
		}
	}
}

func TestMigratedOrderStateGuard(t *testing.T) {
	for _, body := range []string{
		`func complete(){db.Exec("UPDATE order.orders SET status=6")}`,
		`func complete(){q:=db.Table("order.orders AS o"); q.Update("status",6)}`,
		`import ord "ddyidc/internal/order/domain"; func complete(){var o ord.Order; db.Model(&o).Updates(fields)}`,
		`import "ddyidc/internal/order/domain"; func complete(){var o order.Order; db.Save(&o)}`,
	} {
		found, err := inspectSource("internal/cdnsell/service.go", []byte("package cdnsell\n"+body))
		if err != nil || len(found) == 0 {
			t.Fatalf("order-state bypass accepted: %v", err)
		}
	}
	found, err := inspectSource("internal/cdnsell/service.go", []byte(`package cdnsell
import orderstate "ddyidc/internal/order/repository"
func complete(){orderstate.CompletePaidTx(tx,id,customer,now)}`))
	if err != nil || len(found) != 0 {
		t.Fatalf("owned transaction rejected: %v %v", found, err)
	}
}
