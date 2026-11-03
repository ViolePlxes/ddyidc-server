package portal

import (
	_ "embed"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
)

//go:embed formal_content.json
var formalContentJSON []byte

type formalPack struct {
	Pages      []Page     `json:"pages"`
	Categories []Category `json:"categories"`
	Navigation []Link     `json:"navigation"`
	Footer     []Link     `json:"footer"`
}

func placeholderPage(p Page) bool {
	text := p.Title + " " + p.SEO.Description
	for _, m := range p.Modules {
		text += " " + m.Title + " " + m.Text
	}
	for _, word := range []string{"本地演示", "本地页面布局演示", "本地CMS联动测试", "隔离闭环验证", "仅供本地布局", "本段仅用于验证", "仅供界面与只读查询"} {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

// FormalDraft only prepares an editable document. It never saves or publishes,
// never queries customers, and never modifies product/pricing/payment state.
func FormalDraft(input Document) (Document, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return input, err
	}
	var d Document
	if err = json.Unmarshal(raw, &d); err != nil {
		return input, err
	}
	var pack formalPack
	if err = json.Unmarshal(formalContentJSON, &pack); err != nil {
		return input, err
	}
	var oldSeed Document
	if err = json.Unmarshal(seedJSON, &oldSeed); err != nil {
		return input, err
	}
	seedPages := map[string]Page{}
	for _, p := range oldSeed.Pages {
		seedPages[p.Path] = p
	}
	byPath := map[string]Page{}
	demo := false
	kept := []Page{}
	for _, p := range d.Pages {
		isDemo := placeholderPage(p)
		if isDemo {
			demo = true
		}
		if strings.HasPrefix(p.ID, "lab-") && (strings.HasPrefix(p.Path, "/news/local-") || strings.HasPrefix(p.Path, "/help/local-") || p.Path == "/products/900000000001") {
			demo = true
			continue
		}
		kept = append(kept, p)
		byPath[p.Path] = p
	}
	d.Pages = kept
	for _, p := range pack.Pages {
		existing, found := byPath[p.Path]
		empty := found
		for _, m := range existing.Modules {
			if strings.TrimSpace(m.Text) != "" || len(m.Items) > 0 || len(m.RichContent) > 0 || m.Image != "" || m.Href != "" {
				empty = false
			}
		}
		seed, sameSeed := seedPages[p.Path]
		existing.UpdatedAt = ""
		seed.UpdatedAt = ""
		replace := found && (placeholderPage(existing) || empty || (sameSeed && reflect.DeepEqual(existing, seed)))
		if found && !replace {
			continue
		}
		if found {
			p.ID = existing.ID
			for i := range d.Pages {
				if d.Pages[i].Path == p.Path {
					d.Pages[i] = p
					break
				}
			}
		} else {
			d.Pages = append(d.Pages, p)
		}
	}
	if len(d.Pages) > 500 {
		return input, errors.New("正式内容加入后页面数量超过500，请先整理页面")
	}
	for _, c := range pack.Categories {
		found := false
		for _, old := range d.Categories {
			if old.ID == c.ID {
				found = true
			}
		}
		if !found {
			d.Categories = append(d.Categories, c)
		}
	}
	used := map[string]bool{}
	for _, p := range d.Pages {
		used[p.CategoryID] = true
	}
	cats := []Category{}
	for _, c := range d.Categories {
		if !strings.HasPrefix(c.ID, "lab-") || used[c.ID] {
			cats = append(cats, c)
		}
	}
	d.Categories = cats
	if demo || len(d.Site.Navigation) == 0 || reflect.DeepEqual(d.Site.Navigation, oldSeed.Site.Navigation) {
		d.Site.Navigation = pack.Navigation
	}
	if demo || len(d.Site.Footer) == 0 || reflect.DeepEqual(d.Site.Footer, oldSeed.Site.Footer) {
		d.Site.Footer = pack.Footer
	}
	if d.Site.Copyright == oldSeed.Site.Copyright || d.Site.Copyright == "多多云 版权所有" {
		d.Site.Copyright = ""
	}
	for _, old := range []string{"DDYIDC", "DDOIDC"} {
		d.Site.Brand = strings.ReplaceAll(d.Site.Brand, old, "多多云")
		d.Site.Copyright = strings.ReplaceAll(d.Site.Copyright, old, "多多云")
		d.Site.SEO.Title = strings.ReplaceAll(d.Site.SEO.Title, old, "多多云")
		d.Site.SEO.Description = strings.ReplaceAll(d.Site.SEO.Description, old, "多多云")
		d.Site.SEO.Keywords = strings.ReplaceAll(d.Site.SEO.Keywords, old, "多多云")
	}
	if err = Validate(d); err != nil {
		return input, err
	}
	return d, nil
}
