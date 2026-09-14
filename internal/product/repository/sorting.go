package product

import (
	"context"
	dom "ddyidc/internal/product/domain"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"sort"
)

// Both catalogue lists group products identically, with the same per-group tie breaker.
const catalogSort = `COALESCE(parent.sort,g.sort,2147483647) ASC,
 COALESCE(parent.id,g.id,0) ASC, CASE WHEN g.parent_id>0 THEN g.sort ELSE 0 END ASC,
 g.id ASC,p.sort ASC,p.id DESC`

func sameIDs(expected, supplied []int64) bool {
	if len(expected) != len(supplied) || hasDuplicateIDs(supplied) {
		return false
	}
	set := make(map[int64]bool, len(expected))
	for _, id := range expected {
		set[id] = true
	}
	for _, id := range supplied {
		if !set[id] {
			return false
		}
	}
	return true
}

// Lock in ID order to serialize moves and sibling sorting, including both parents.
// InnoDB range locks also prevent an insertion from invalidating full-list validation.
func lockGroups(tx *gorm.DB) ([]dom.ProductGroup, error) {
	var groups []dom.ProductGroup
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=0").Order("id ASC").Find(&groups).Error
	return groups, err
}
func siblingIDs(groups []dom.ProductGroup, parent int64) []int64 {
	siblings := make([]dom.ProductGroup, 0)
	for _, g := range groups {
		if g.ParentID == parent {
			siblings = append(siblings, g)
		}
	}
	sort.Slice(siblings, func(i, j int) bool {
		if siblings[i].Sort == siblings[j].Sort {
			return siblings[i].ID < siblings[j].ID
		}
		return siblings[i].Sort < siblings[j].Sort
	})
	ids := make([]int64, 0, len(siblings))
	for _, g := range siblings {
		ids = append(ids, g.ID)
	}
	return ids
}
func rewriteSort(tx *gorm.DB, table string, ids []int64) error {
	for index, id := range ids {
		if err := tx.Table(table).Where("id=?", id).Update("sort", index).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *GormRepository) SortAdminGroups(ctx context.Context, parent int64, ids []int64) error {
	if len(ids) == 0 {
		return ErrInvalidSort
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groups, err := lockGroups(tx)
		if err != nil {
			return err
		}
		if !sameIDs(siblingIDs(groups, parent), ids) {
			return ErrInvalidSort
		}
		return rewriteSort(tx, groupTable, ids)
	})
}

func (r *GormRepository) MoveAdminGroup(ctx context.Context, id, targetParent int64, targetIDs, sourceIDs []int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groups, err := lockGroups(tx)
		if err != nil {
			return err
		}
		var moving, target, source *dom.ProductGroup
		for i := range groups {
			g := &groups[i]
			if g.ID == id {
				moving = g
			}
			if g.ID == targetParent {
				target = g
			}
		}
		if moving == nil || target == nil {
			return ErrGroupNotFound
		}
		if moving.ParentID == 0 || target.ParentID != 0 || moving.ParentID == targetParent {
			return ErrGroupMove
		}
		for i := range groups {
			if groups[i].ID == moving.ParentID {
				source = &groups[i]
				break
			}
		}
		if source == nil || source.ParentID != 0 {
			return ErrGroupMove
		}
		expectedTarget := append(siblingIDs(groups, targetParent), id)
		expectedSource := make([]int64, 0)
		for _, sid := range siblingIDs(groups, moving.ParentID) {
			if sid != id {
				expectedSource = append(expectedSource, sid)
			}
		}
		if !sameIDs(expectedTarget, targetIDs) || !sameIDs(expectedSource, sourceIDs) {
			return ErrInvalidSort
		}
		// Source siblings must retain their relative order; clients cannot silently reorder them.
		for i, sid := range expectedSource {
			if sourceIDs[i] != sid {
				return ErrInvalidSort
			}
		}
		if err = tx.Table(groupTable).Where("id=?", id).Update("parent_id", targetParent).Error; err != nil {
			return err
		}
		if err = rewriteSort(tx, groupTable, targetIDs); err != nil {
			return err
		}
		return rewriteSort(tx, groupTable, sourceIDs)
	})
}

func (r *GormRepository) SortProducts(ctx context.Context, groupID int64, ids []int64) error {
	if len(ids) == 0 {
		return ErrInvalidProducts
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := validateSecondLevelGroup(tx, groupID); err != nil {
			return err
		}
		var products []dom.Product
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id=?", groupID).Order("id ASC").Find(&products).Error; err != nil {
			return err
		}
		expected := make([]int64, 0, len(products))
		for _, p := range products {
			expected = append(expected, p.ID)
		}
		if !sameIDs(expected, ids) {
			return ErrInvalidProducts
		}
		return rewriteSort(tx, productTable, ids)
	})
}
