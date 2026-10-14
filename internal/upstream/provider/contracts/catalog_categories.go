package contracts

type CatalogCategory struct {
	ID       string `json:"id"`
	ParentID string `json:"parentId"`
	Name     string `json:"name"`
}

type CatalogCategoryProvider interface {
	GetProductCategories() ([]CatalogCategory, error)
}
