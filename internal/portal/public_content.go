package portal

import (
	"net/http"
	"net/url"
	"strings"
)

// PublicContent is a single-revision projection, never a draft document.
type PublicContent struct {
	Site       Site       `json:"site"`
	Pages      []Page     `json:"pages"`
	Categories []Category `json:"categories"`
	Revision   int64      `json:"revision"`
}

func publicContent(snap Snapshot) PublicContent {
	out := PublicContent{Site: snap.Document.Site, Revision: snap.Document.Revision, Pages: []Page{}}
	referenced := map[string]bool{}
	hidden := map[string]bool{}
	for _, page := range snap.Document.Pages {
		if !page.Enabled {
			hidden[page.Path] = true
		}
	}
	for _, page := range pagesFor(snap) {
		modules := []Module{}
		for _, module := range page.Modules {
			if module.Enabled {
				modules = append(modules, module)
			}
		}
		page.Modules = modules
		out.Pages = append(out.Pages, page)
		if page.Kind == "article" && page.CategoryID != "" {
			referenced[page.CategoryID] = true
		}
	}
	for _, category := range snap.Document.Categories {
		if referenced[category.ID] {
			out.Categories = append(out.Categories, category)
		}
	}
	var links func([]Link) []Link
	links = func(input []Link) []Link {
		out := []Link{}
		for _, link := range input {
            if link.Enabled!=nil && !*link.Enabled {continue}
			path := strings.SplitN(strings.SplitN(link.Href, "?", 2)[0], "#", 2)[0]
			if target, err := url.Parse(link.Href); err == nil && target.IsAbs() {
				if origin, err := url.Parse(snap.Document.Site.Origin); err == nil && strings.EqualFold(target.Host, origin.Host) {
					path = target.Path
				}
			}
			if hidden[path] {
				continue
			}
			link.Children = links(link.Children)
			out = append(out, link)
		}
		return out
	}
	out.Site.Navigation = links(out.Site.Navigation)
	out.Site.Footer = links(out.Site.Footer)
	return out
}
func (s *Service) publicContent(w http.ResponseWriter, r *http.Request) {
	snap, _, err := s.Current(r.Context())
	if err != nil || !snap.Document.Site.Enabled {
		reply(w, http.StatusServiceUnavailable, nil, "公开内容暂时不可用")
		return
	}
	reply(w, http.StatusOK, publicContent(snap), "")
}
