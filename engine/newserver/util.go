package newserver

import (
	"errors"

	"github.com/containerd/containerd/platforms"
	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/cmd/buildkitd/config"
	"github.com/moby/buildkit/util/entitlements"
	bkworker "github.com/moby/buildkit/worker"
	ocispecs "github.com/opencontainers/image-spec/specs-go/v1"
)

/*
Buildkit's worker.Controller is a bit odd; it exists to manage multiple workers because that was
a planned feature years ago, but it never got implemented. So it exists to manage a single worker,
which doesn't really add much.

We still need to provide a worker.Controller value to a few places though, which this method enables.
*/
func asWorkerController(w bkworker.Worker) (*bkworker.Controller, error) {
	wc := &bkworker.Controller{}
	err := wc.Add(w)
	if err != nil {
		return nil, err
	}
	return wc, nil
}

func parsePlatforms(platformsStr []string) ([]ocispecs.Platform, error) {
	out := make([]ocispecs.Platform, 0, len(platformsStr))
	for _, s := range platformsStr {
		p, err := platforms.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, platforms.Normalize(p))
	}
	return out, nil
}

func formatPlatforms(p []ocispecs.Platform) []string {
	str := make([]string, 0, len(p))
	for _, pp := range p {
		str = append(str, platforms.Format(platforms.Normalize(pp)))
	}
	return str
}

func toEntitlementStrings(ents entitlements.Set) []string {
	var out []string
	for ent := range ents {
		out = append(out, string(ent))
	}
	return out
}

func getGCPolicy(cfg config.GCConfig, root string) []client.PruneInfo {
	if cfg.GC != nil && !*cfg.GC {
		return nil
	}
	if len(cfg.GCPolicy) == 0 {
		cfg.GCPolicy = config.DefaultGCPolicy(cfg.GCKeepStorage)
	}
	out := make([]client.PruneInfo, 0, len(cfg.GCPolicy))
	for _, rule := range cfg.GCPolicy {
		out = append(out, client.PruneInfo{
			Filter:       rule.Filters,
			All:          rule.All,
			KeepBytes:    rule.KeepBytes.AsBytes(root),
			KeepDuration: rule.KeepDuration.Duration,
		})
	}
	return out
}

type cleanups struct {
	funcs []func() error
}

func (c *cleanups) add(f func() error) {
	c.funcs = append(c.funcs, f)
}

func (c *cleanups) addNoErr(f func()) {
	c.add(func() error {
		f()
		return nil
	})
}

func (c *cleanups) run() error {
	var rerr error
	for i := len(c.funcs) - 1; i >= 0; i-- {
		if err := c.funcs[i](); err != nil {
			rerr = errors.Join(rerr, err)
		}
	}
	return rerr
}
