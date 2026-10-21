package layers

import (
	"encoding/json"
	"slices"

	"github.com/goxang/rig-plugins/internal/orgkit"
	"github.com/goxang/rig/sdk"
)

// pages are the portal's tabs and the exposure names each shows; plugins.org-portal.pages replaces them.
var pages = map[string][]string{
	"Status":   {"service-status", "payments-flow"},
	"Roadmap":  {"public-roadmap"},
	"Releases": {"release-notes"},
	"Ask":      {"submit-request"},
}

// Portal is org-portal: what the company shows people outside it (a status page, the public roadmap,
// release notes, a way to ask), each tab made of what the units below expose to stakeholders.
func Portal(p *sdk.Plugin) {
	for name := range pages {
		page(p, name, func(c *sdk.Ctx) (*sdk.Node, error) {
			names := pages[name]
			var cfg struct {
				Pages map[string][]string `json:"pages"`
			}
			b, _ := json.Marshal(c.Config().Config)
			if json.Unmarshal(b, &cfg) == nil && cfg.Pages[name] != nil {
				names = cfg.Pages[name]
			}
			return live(c, nil, func(r map[string]any) bool { return slices.Contains(names, orgkit.Str(r["name"])) })
		})
	}
}
