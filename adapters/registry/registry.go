// Package registry is registry/1: an image registry's tags, and pruning all but the newest per image.
// It speaks the Docker registry API (any OCI registry, Nexus docker repositories included) or Nexus's
// own REST API, which lists and deletes a whole repository's components at once.
package registry

import (
	"cmp"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/goxang/rig-plugins/internal/forge"
	"github.com/goxang/rig-plugins/internal/frames"
	"github.com/goxang/rig-plugins/internal/httpx"
	"github.com/goxang/rig/sdk"
)

type Tag struct {
	Image   string    `json:"image"`
	Tag     string    `json:"tag"`
	Digest  string    `json:"digest"`
	Created time.Time `json:"created"`
	Size    int64     `json:"size"`
	id      string
}

type backend interface {
	Tags(ctx context.Context, repository string) ([]Tag, error)
	Delete(ctx context.Context, repository string, t Tag) error
}

// Doomed are the tags to delete: per image, all but the keep newest that match; a digest a kept
// tag also points at is never deleted.
func Doomed(tags []Tag, keep int, match *regexp.Regexp) []Tag {
	byImage := map[string][]Tag{}
	for _, t := range tags {
		if match == nil || match.MatchString(t.Tag) {
			byImage[t.Image] = append(byImage[t.Image], t)
		}
	}
	kept := map[string]bool{}
	var out []Tag
	for _, img := range slices.Sorted(func(yield func(string) bool) {
		for k := range byImage {
			if !yield(k) {
				return
			}
		}
	}) {
		ts := byImage[img]
		slices.SortStableFunc(ts, func(a, b Tag) int { return b.Created.Compare(a.Created) })
		for i, t := range ts {
			if i < keep {
				kept[t.Digest] = true
			} else {
				out = append(out, t)
			}
		}
	}
	return slices.DeleteFunc(out, func(t Tag) bool { return t.Digest != "" && kept[t.Digest] })
}

// nexus is Nexus Repository's components API: repository is the Nexus repository name.
type nexus struct{ *forge.Client }

func (n nexus) Tags(ctx context.Context, repository string) ([]Tag, error) {
	var out []Tag
	token := ""
	for {
		q := url.Values{"repository": {repository}}
		if token != "" {
			q.Set("continuationToken", token)
		}
		var page struct {
			Items []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Version string `json:"version"`
				Assets  []struct {
					LastModified time.Time         `json:"lastModified"`
					FileSize     int64             `json:"fileSize"`
					Checksum     map[string]string `json:"checksum"`
				} `json:"assets"`
			} `json:"items"`
			ContinuationToken string `json:"continuationToken"`
		}
		if err := n.Get(ctx, "/service/rest/v1/components?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		for _, it := range page.Items {
			t := Tag{Image: it.Name, Tag: it.Version, id: it.ID}
			for _, a := range it.Assets {
				if a.LastModified.After(t.Created) {
					t.Created = a.LastModified
				}
				t.Size += a.FileSize
				if d := a.Checksum["sha256"]; d != "" && t.Digest == "" {
					t.Digest = "sha256:" + d
				}
			}
			out = append(out, t)
		}
		if token = page.ContinuationToken; token == "" {
			return out, nil
		}
	}
}

func (n nexus) Delete(ctx context.Context, _ string, t Tag) error {
	return n.Send(ctx, "DELETE", "/service/rest/v1/components/"+url.PathEscape(t.id), nil, nil)
}

// docker is the registry API: repository is an image path (registry.corp/team/app).
type docker struct {
	opts     []remote.Option
	nameOpts []name.Option
}

func (d docker) Tags(ctx context.Context, repository string) ([]Tag, error) {
	repo, err := name.NewRepository(repository, d.nameOpts...)
	if err != nil {
		return nil, err
	}
	opts := append([]remote.Option{remote.WithContext(ctx)}, d.opts...)
	names, err := remote.List(repo, opts...)
	if err != nil {
		return nil, err
	}
	out := make([]Tag, len(names))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, tag := range names {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = Tag{Image: repo.RepositoryStr(), Tag: tag}
			img, err := remote.Image(repo.Tag(tag), opts...)
			if err != nil {
				return
			}
			if dg, err := img.Digest(); err == nil {
				out[i].Digest = dg.String()
			}
			if cf, err := img.ConfigFile(); err == nil {
				out[i].Created = cf.Created.Time
			}
			if m, err := img.Manifest(); err == nil {
				for _, l := range m.Layers {
					out[i].Size += l.Size
				}
			}
		}()
	}
	wg.Wait()
	return out, nil
}

func (d docker) Delete(ctx context.Context, repository string, t Tag) error {
	repo, err := name.NewRepository(repository, d.nameOpts...)
	if err != nil {
		return err
	}
	return remote.Delete(repo.Digest(t.Digest), append([]remote.Option{remote.WithContext(ctx)}, d.opts...)...)
}

// open reads plugins.registry: type (docker or nexus), url (Nexus's), user, password
// (REGISTRY_PASSWORD or NEXUS_PASSWORD), insecure (plain HTTP or an unverified certificate).
func open(c *sdk.Ctx) (backend, error) {
	user := forge.Config(c, "user")
	pass := forge.Config(c, "password")
	if pass == "" {
		pass = forge.Secret(c, "REGISTRY_PASSWORD", "NEXUS_PASSWORD")
	}
	insecure := forge.Config(c, "insecure") == "true"
	switch forge.Config(c, "type") {
	case "nexus":
		base := strings.TrimSuffix(forge.Config(c, "url"), "/")
		if base == "" {
			return nil, sdk.Unavailable("the registry has no address", "set plugins.registry.url (Nexus's address)")
		}
		return nexus{&forge.Client{Base: base, Endpoint: httpx.Endpoint{User: user, Password: pass}}}, nil
	case "", "docker":
		d := docker{}
		if user != "" {
			d.opts = append(d.opts, remote.WithAuth(&authn.Basic{Username: user, Password: pass}))
		} else {
			d.opts = append(d.opts, remote.WithAuthFromKeychain(authn.DefaultKeychain))
		}
		if insecure {
			d.nameOpts = append(d.nameOpts, name.Insecure)
			tr := http.DefaultTransport.(*http.Transport).Clone()
			tr.TLSClientConfig.InsecureSkipVerify = true
			d.opts = append(d.opts, remote.WithTransport(tr))
		}
		return d, nil
	default:
		return nil, fmt.Errorf("plugins.registry.type is docker or nexus")
	}
}

type pruneArgs struct {
	Repository string `json:"repository" rig:"required" desc:"an image path, or a Nexus repository name"`
	Keep       int    `json:"keep" desc:"newest tags kept per image (default 5)"`
	Match      string `json:"match" desc:"only tags matching this regex are counted and pruned"`
}

func (a pruneArgs) doomed(c *sdk.Ctx) (backend, []Tag, error) {
	b, err := open(c)
	if err != nil {
		return nil, nil, err
	}
	var re *regexp.Regexp
	if a.Match != "" {
		if re, err = regexp.Compile(a.Match); err != nil {
			return nil, nil, err
		}
	}
	tags, err := b.Tags(c, a.Repository)
	if err != nil {
		return nil, nil, err
	}
	return b, Doomed(tags, cmp.Or(a.Keep, 5), re), nil
}

func Register(p *sdk.Plugin) {
	p.Source("registry.tags", func(c *sdk.Ctx, in struct {
		Repository string `json:"repository" rig:"required" desc:"an image path, or a Nexus repository name"`
	}) (*sdk.Frame, error) {
		b, err := open(c)
		if err != nil {
			return nil, err
		}
		tags, err := b.Tags(c, in.Repository)
		slices.SortFunc(tags, func(a, b Tag) int { return cmp.Or(strings.Compare(a.Image, b.Image), b.Created.Compare(a.Created)) })
		return frames.Of(tags), err
	}, sdk.Cache("60s"))

	p.Action("registry.prune", sdk.EffectDanger, func(c *sdk.Ctx, in pruneArgs) (*sdk.Result, error) {
		if in.Keep < 0 {
			return nil, fmt.Errorf("keep must be at least 1")
		}
		b, doomed, err := in.doomed(c)
		if err != nil {
			return nil, err
		}
		failed := 0
		for i, t := range doomed {
			if err := b.Delete(c, in.Repository, t); err != nil {
				failed++
				c.Line(fmt.Sprintf("[%d/%d] FAIL %s:%s: %v", i+1, len(doomed), t.Image, t.Tag, err))
				continue
			}
			c.Line(fmt.Sprintf("[%d/%d] deleted %s:%s", i+1, len(doomed), t.Image, t.Tag))
		}
		msg := fmt.Sprintf("deleted %d tags from %s", len(doomed)-failed, in.Repository)
		if failed > 0 {
			return nil, fmt.Errorf("%s; %d failed", msg, failed)
		}
		return &sdk.Result{Message: msg}, nil
	}, sdk.Describe("delete all but the newest tags per image"))

	p.Plan("registry.prune", func(c *sdk.Ctx, in pruneArgs) (*sdk.Result, error) {
		_, doomed, err := in.doomed(c)
		if err != nil {
			return nil, err
		}
		res := &sdk.Result{Message: fmt.Sprintf("would delete %d tags", len(doomed))}
		for _, t := range doomed {
			res.Changes = append(res.Changes, sdk.Change{Op: sdk.OpDelete, Target: t.Image + ":" + t.Tag})
		}
		return res, nil
	})
}
