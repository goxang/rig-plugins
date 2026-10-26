package main

import (
	"context"
	"embed"
	"maps"

	"github.com/goxang/rig-plugins/adapters/cloud/aws"
	"github.com/goxang/rig-plugins/adapters/docs/confluence"
	"github.com/goxang/rig-plugins/adapters/docs/markdown"
	"github.com/goxang/rig-plugins/adapters/flow"
	"github.com/goxang/rig-plugins/adapters/iac/helm"
	"github.com/goxang/rig-plugins/adapters/iac/terraform"
	"github.com/goxang/rig-plugins/adapters/layers"
	"github.com/goxang/rig-plugins/adapters/notify/mail"
	"github.com/goxang/rig-plugins/adapters/notify/slack"
	"github.com/goxang/rig-plugins/adapters/org"
	"github.com/goxang/rig-plugins/adapters/registry"
	"github.com/goxang/rig-plugins/adapters/scm/github"
	"github.com/goxang/rig-plugins/adapters/scm/gitlab"
	"github.com/goxang/rig-plugins/adapters/tracker/jira"
	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/forge"
	imp "github.com/goxang/rig-plugins/internal/importers"
	"github.com/goxang/rig/core"
	"github.com/goxang/rig/sdk"
)

// overlays are manifest keys beyond what the code declares: config schema, secrets, detect, views.
//
//go:embed manifests/*.yaml
var overlays embed.FS

// contractsOf are the contracts plugins serve through their own sources and actions.
var contractsOf = map[string][]string{
	"git":           {"vcs/1"},
	"gitlab":        {"scm/1", "tracker/1"},
	"github":        {"scm/1", "tracker/1"},
	"jira":          {"tracker/1"},
	"confluence":    {"docs/1"},
	"markdown-docs": {"docs/1"},
	"terraform":     {"iac/1"},
	"registry":      {"registry/1"},
	"slack":         {"notify/1"},
	"mail":          {"notify/1"},
	"org":           {"unit/1"},
}

func source(ctx context.Context, ref string) (string, error) {
	a, err := app()
	if err != nil {
		return "", err
	}
	return a.Source(ctx, ref)
}

func affected(ctx context.Context, files []string) ([]string, error) {
	a, err := app()
	if err != nil {
		return nil, err
	}
	return a.Affected(ctx, files), nil
}

func init() {
	plugins["aws"] = []served{{core.KindMetrics, (*aws.AWS)(nil)}, {core.KindLogs, (*aws.AWS)(nil)}}
	maps.Copy(actions, map[string]func(*sdk.Plugin){
		"git":            func(p *sdk.Plugin) { git.Register(p, source) },
		"gitlab":         func(p *sdk.Plugin) { forge.RegisterSCM(p, gitlab.Open, affected) },
		"github":         func(p *sdk.Plugin) { forge.RegisterSCM(p, github.Open, affected) },
		"jira":           jira.Register,
		"confluence":     confluence.Register,
		"markdown-docs":  markdown.Register,
		"terraform":      terraform.Register,
		"helm":           helm.Register,
		"aws":            aws.Register,
		"slack":          slack.Register,
		"mail":           mail.Register,
		"registry":       registry.Register,
		"org":            org.Register,
		"flow":           flow.Register,
		"org-management": layers.Management,
		"org-product":    layers.Product,
		"org-technology": layers.Technology,
		"org-people":     layers.People,
		"org-executive":  layers.Executive,
		"org-portal":     layers.Portal,
		"org-delivery":   layers.Delivery,
	})
	maps.Copy(importers, map[string]map[string]importer{
		"gitlab":      {"gitlab-ci": {[]string{".gitlab-ci.yml"}, imp.GitLabCI}},
		"github":      {"github-actions": {[]string{".github/workflows"}, imp.GitHubActions}},
		"just":        {"just": {[]string{"justfile", "Justfile", ".justfile"}, imp.Just}},
		"make":        {"make": {[]string{"Makefile", "makefile", "GNUmakefile"}, imp.Make}},
		"npm-scripts": {"npm-scripts": {[]string{"package.json"}, imp.NPM}},
		"procfile":    {"procfile": {[]string{"Procfile"}, imp.Procfile}},
		"dotenv":      {"dotenv": {[]string{".env"}, imp.Dotenv}},
		"grafana":     {"grafana": {[]string{"grafana", "dashboards"}, imp.Grafana}},
		"openapi":     {"openapi": {[]string{"openapi.yaml", "openapi.json", "swagger.yaml", "swagger.json"}, imp.OpenAPI}},
		"prometheus":  {"prometheus-rules": {[]string{"rules.yml", "alerts.yml", "prometheus/rules"}, imp.PrometheusRules}},
	})
}
