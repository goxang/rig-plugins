// Package forge is what the hosted-service plugins (gitlab, github, jira, confluence, slack) share:
// their config, token and JSON calls.
package forge

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

// Client is one service's API at Base, authenticated.
type Client struct {
	Base string
	// Project is the repository path (group/app) for a forge, from config or the origin remote.
	Project string
	// Host is the service's web host, for links.
	Host string
	httpx.Endpoint
}

// Config reads a string key of the plugin's config.
func Config(c *sdk.Ctx, key string) string {
	if v, ok := c.Config().Config[key]; ok && v != nil {
		return fmt.Sprint(v)
	}
	return ""
}

// Token is config `token`, else the first of the secrets rig has a value for, else the same names in the environment.
func Token(c *sdk.Ctx, secrets ...string) string {
	if t := Config(c, "token"); t != "" {
		return t
	}
	return Secret(c, secrets...)
}

// Secret is the first of the secrets rig has a value for, else the same names in the environment.
func Secret(c *sdk.Ctx, secrets ...string) string {
	for _, s := range secrets {
		if v, err := c.Secret(s); err == nil && v != "" {
			return v
		}
	}
	for _, s := range secrets {
		if v := os.Getenv(s); v != "" {
			return v
		}
	}
	return ""
}

// Repo is a forge's web host and project path: config `url` and `project`, else the origin remote.
func Repo(c *sdk.Ctx, defaultHost string) (host, project string, err error) {
	host, project = strings.TrimSuffix(Config(c, "url"), "/"), Config(c, "project")
	if host == "" || project == "" {
		h, p, rerr := git.Remote(c, "origin")
		if rerr != nil && project == "" {
			return "", "", fmt.Errorf("set plugins.<name>.project, or add an origin remote: %w", rerr)
		}
		if project == "" {
			project = p
		}
		if host == "" && h != "" {
			host = "https://" + h
		}
	}
	if host == "" {
		host = defaultHost
	}
	return host, project, nil
}

func (c *Client) Get(ctx context.Context, path string, out any) error {
	return c.DoURL(ctx, "GET", c.Base+path, nil, out)
}

func (c *Client) Send(ctx context.Context, method, path string, body, out any) error {
	if body == nil {
		body = map[string]any{}
	}
	return c.DoURL(ctx, method, c.Base+path, body, out)
}

// Text is a plain-text answer (a job log), as lines.
func (c *Client) Text(ctx context.Context, path string) ([]string, error) {
	var raw []byte
	if err := c.DoURL(ctx, "GET", c.Base+path, nil, &raw); err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n"), nil
}

// Affected names the project's services that files touch (the core engine knows); nil when unknown.
type Affected func(ctx context.Context, files []string) ([]string, error)

// Lines sends a run's text to its output.
func Lines(c *sdk.Ctx, ls []string) {
	for _, l := range ls {
		c.Line(l)
	}
}
