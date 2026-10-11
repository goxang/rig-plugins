package main

import (
	"fmt"

	"github.com/goxang/rig-plugins/internal/ide"
	"github.com/goxang/rig/sdk"
)

// actions are plugin actions outside any contract, by plugin.
var actions = map[string]func(*sdk.Plugin){
	"goland": func(p *sdk.Plugin) {
		p.Action("ide", sdk.EffectWrite, func(_ *sdk.Ctx, in struct {
			Services []string `json:"services"`
			Open     bool     `json:"open"`
		}) (*sdk.Result, error) {
			a, err := app()
			if err != nil {
				return nil, err
			}
			names := in.Services
			if len(names) == 0 {
				names = a.Spec.ServiceNames()
			}
			r, err := ide.Write(a, names)
			if err != nil {
				return nil, err
			}
			msg := fmt.Sprintf("GoLand: %d services (%d debuggable) in %d folders under .idea/runConfigurations; Services view (alt+8): run one for its live logs, stop stops it, · debug attaches", r.Services, r.Debug, len(r.Folders))
			if r.VSCode != "" {
				msg += "; VS Code attach configs in " + r.VSCode
			}
			if !r.Dashboard {
				msg += "; add them to the Services view: + › Run Configuration Type › Shell Script and Go Remote"
			}
			if in.Open {
				if err := ide.Open(a.Spec.Dir); err != nil {
					msg += "; " + err.Error()
				}
			}
			return &sdk.Result{Message: msg}, nil
		})
	},
}
