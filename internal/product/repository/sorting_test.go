package product

import (
	"context"
	dom "ddyidc/internal/product/domain"
	"errors"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"os"
	"reflect"
	"testing"
)

func TestFullListValidation(t *testing.T) {
	for _, tc := range []struct {
		ids   []int64
		valid bool
	}{
		{[]int64{3, 2, 1}, true}, {nil, false}, {[]int64{1, 2}, false}, {[]int64{1, 1, 3}, false}, {[]int64{1, 2, 4}, false}, {[]int64{1, 2, 0}, false},
	} {
		if got := sameIDs([]int64{1, 2, 3}, tc.ids); got != tc.valid {
			t.Fatalf("ids %v valid=%v", tc.ids, got)
		}
	}
}

// Real MySQL, with all fixtures and mutations contained in an outer rollback.
func TestMoveGroupMySQL(t *testing.T) {
	dsn := os.Getenv("PRODUCT_SORT_TEST_DSN")
	if dsn == "" {
		t.Skip("PRODUCT_SORT_TEST_DSN not set")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal("cannot open integration database")
	}
	tx := db.Begin()
	if tx.Error != nil {
		t.Fatal(tx.Error)
	}
	defer tx.Rollback()
	repo := &GormRepository{db: tx}
	ctx := context.Background()
	create := func(parent int64, name string) *dom.ProductGroup {
		g := &dom.ProductGroup{ParentID: parent, Name: name, Status: 1}
		if err := repo.CreateAdminGroup(ctx, g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	a, b := create(0, "sort integration A"), create(0, "sort integration B")
	x, y, z := create(a.ID, "X"), create(a.ID, "Y"), create(b.ID, "Z")
	if y.Sort != x.Sort+1 {
		t.Fatal("creation did not append within parent")
	}
	if err := repo.MoveAdminGroup(ctx, x.ID, b.ID, []int64{z.ID, x.ID}, []int64{y.ID}); err != nil {
		t.Fatal(err)
	}
	var current dom.ProductGroup
	tx.Where("id=?", x.ID).Take(&current)
	if current.ParentID != b.ID || current.Sort != 1 {
		t.Fatal("move did not persist parent and position")
	}
	groups, _ := lockGroups(tx)
	if !reflect.DeepEqual(siblingIDs(groups, a.ID), []int64{y.ID}) {
		t.Fatal("source mismatch")
	}
	if err := repo.MoveAdminGroup(ctx, x.ID, a.ID, []int64{x.ID}, []int64{z.ID}); !errors.Is(err, ErrInvalidSort) {
		t.Fatal("missing target sibling accepted")
	}
	if err := repo.MoveAdminGroup(ctx, a.ID, b.ID, []int64{a.ID, z.ID, x.ID}, nil); !errors.Is(err, ErrGroupMove) {
		t.Fatal("root move accepted")
	}
	if err := repo.MoveAdminGroup(ctx, x.ID, y.ID, []int64{x.ID}, []int64{z.ID}); !errors.Is(err, ErrGroupMove) {
		t.Fatal("third-level move accepted")
	}
	if err := repo.SortAdminGroups(ctx, b.ID, []int64{z.ID, y.ID}); !errors.Is(err, ErrInvalidSort) {
		t.Fatal("cross-level ids accepted")
	}
	if err := repo.SortAdminGroups(ctx, b.ID, nil); !errors.Is(err, ErrInvalidSort) {
		t.Fatal("empty ids accepted")
	}
	if err := repo.SortAdminGroups(ctx, b.ID, []int64{x.ID, z.ID}); err != nil {
		t.Fatal(err)
	}
	// Fail after the parent change and one target sort write; transaction must undo both.
	before, _ := lockGroups(tx)
	writes := 0
	const callback = "sort-test-fail-update"
	if err := db.Callback().Update().Before("gorm:update").Register(callback, func(db *gorm.DB) {
		writes++
		if writes == 3 {
			db.AddError(errors.New("injected test write failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = repo.MoveAdminGroup(ctx, x.ID, a.ID, []int64{x.ID, y.ID}, []int64{z.ID})
	db.Callback().Update().Remove(callback)
	if err == nil || writes < 3 {
		t.Fatal("failure injection did not execute")
	}
	after, _ := lockGroups(tx)
	for i := range before {
		if before[i].ID != after[i].ID || before[i].ParentID != after[i].ParentID || before[i].Sort != after[i].Sort {
			t.Fatal("partial movement survived rollback")
		}
	}
}
