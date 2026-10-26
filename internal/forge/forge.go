// Package forge is what the hosted-service plugins (gitlab, github, jira, confluence, slack) share:
// their config, token and JSON calls.
package forge

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/goxang/rig-plugins/adapters/vcs/git"
	"github.com/goxang/rig-plugins/internal/frames"
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

// Setting is config key, else the secret <PREFIX>_<KEY> a connect form stored.
func Setting(c *sdk.Ctx, prefix, key string) string {
	if v := Config(c, key); v != "" {
		return v
	}
	return Secret(c, prefix+"_"+strings.ToUpper(key))
}

// Account is what a connect form asks of a hosted service; Scope is its project or space key.
type Account struct {
	URL   string `json:"url" title:"Address" rig:"required" desc:"https://your-org.atlassian.net"`
	User  string `json:"user" title:"Email or user" desc:"Cloud: the account email; empty for a Server personal token"`
	Token string `json:"token" title:"API token" format:"password" desc:"empty keeps the stored one"`
	Scope string `json:"scope" title:"Project or space key"`
}

type connection struct {
	Connected bool   `json:"connected"`
	URL       string `json:"url"`
	User      string `json:"user"`
	Scope     string `json:"scope"`
	Message   string `json:"message"`
}

// Connect registers connect, disconnect and connection: a form that keeps a service's account in
// the plugin's secrets PREFIX_URL, _USER, _TOKEN and _<SCOPE>, which the manifest must declare.
// check proves the account works and says who it is.
func Connect(p *sdk.Plugin, prefix, scope string, check func(*sdk.Ctx) (string, error)) {
	keys := []string{"url", "user", "token", scope}
	set := func(c *sdk.Ctx, vals map[string]string) error {
		for _, k := range keys {
			if v, ok := vals[k]; ok {
				if err := c.SetSecret(prefix+"_"+strings.ToUpper(k), v); err != nil {
					return err
				}
			}
		}
		return nil
	}
	p.Action("connect", sdk.EffectWrite, func(c *sdk.Ctx, in Account) (*sdk.Result, error) {
		vals := map[string]string{"url": strings.TrimSuffix(strings.TrimSpace(in.URL), "/"), "user": strings.TrimSpace(in.User), scope: strings.TrimSpace(in.Scope)}
		if in.Token != "" {
			vals["token"] = strings.TrimSpace(in.Token)
		} else if Setting(c, prefix, "token") == "" {
			return nil, fmt.Errorf("an API token is needed")
		}
		if err := set(c, vals); err != nil {
			return nil, err
		}
		who, err := check(c)
		if err != nil {
			return nil, fmt.Errorf("saved, but the service refused it: %w", err)
		}
		return &sdk.Result{Message: "connected as " + who}, nil
	}, sdk.Describe("store the account (address, user, API token) and test it"))
	p.Action("disconnect", sdk.EffectWrite, func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Result, error) {
		return &sdk.Result{Message: "disconnected"}, set(c, map[string]string{"url": "", "user": "", "token": "", scope: ""})
	}, sdk.Describe("forget the stored account"))
	p.Source("connection", func(c *sdk.Ctx, _ sdk.Empty) (*sdk.Frame, error) {
		cn := connection{URL: Setting(c, prefix, "url"), User: Setting(c, prefix, "user"), Scope: Setting(c, prefix, scope)}
		if cn.URL == "" {
			cn.Message = "not connected: fill in the form"
		} else if who, err := check(c); err != nil {
			cn.Message = err.Error()
		} else {
			cn.Connected, cn.Message = true, "connected as "+who
		}
		return frames.Of([]connection{cn}), nil
	}, sdk.Cache("30s"), sdk.Describe("whether the stored account works"))
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
