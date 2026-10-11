// Package slack is notify/1 over Slack: an incoming webhook (SLACK_WEBHOOK_URL), or a bot token
// (SLACK_TOKEN) with chat.postMessage to a channel.
package slack

import (
	"context"
	"fmt"
	"strings"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

// Message is Slack's mrkdwn: the title in bold on its own line, then the body.
func Message(title, body string) string {
	if title == "" {
		return body
	}
	return "*" + title + "*\n" + body
}

// Send posts text through webhook, or with token to channel when there is no webhook.
func Send(ctx context.Context, webhook, token, api, channel, text string) error {
	if webhook != "" {
		var out []byte
		return httpx.Endpoint{}.DoURL(ctx, "POST", webhook, map[string]string{"text": text}, &out)
	}
	if token == "" {
		return fmt.Errorf("set SLACK_WEBHOOK_URL, or SLACK_TOKEN and a channel")
	}
	if channel == "" {
		return fmt.Errorf("which channel? pass to, or set plugins.slack.channel")
	}
	var res struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	err := httpx.Endpoint{Token: token}.DoURL(ctx, "POST", api+"/chat.postMessage", map[string]string{"channel": channel, "text": text}, &res)
	if err == nil && !res.OK {
		err = fmt.Errorf("slack: %s", res.Error)
	}
	return err
}

func Register(p *sdk.Plugin) {
	p.Action("notify.send", sdk.EffectWrite, func(c *sdk.Ctx, in struct {
		To    string `json:"to" desc:"a channel (default plugins.slack.channel)"`
		Title string `json:"title"`
		Body  string `json:"body" rig:"required"`
	}) (*sdk.Result, error) {
		webhook := forge.Config(c, "webhook")
		if webhook == "" {
			webhook = forge.Secret(c, "SLACK_WEBHOOK_URL")
		}
		channel := in.To
		if channel == "" {
			channel = forge.Config(c, "channel")
		}
		// a webhook posts to its own channel; only a token can pick another
		if in.To != "" {
			if tok := forge.Token(c, "SLACK_TOKEN"); tok != "" {
				webhook = ""
			}
		}
		api := strings.TrimSuffix(forge.Config(c, "api"), "/")
		if api == "" {
			api = "https://slack.com/api"
		}
		if err := Send(c, webhook, forge.Token(c, "SLACK_TOKEN"), api, channel, Message(in.Title, in.Body)); err != nil {
			return nil, err
		}
		return &sdk.Result{Message: "sent"}, nil
	})
}
