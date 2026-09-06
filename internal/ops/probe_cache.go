package ops

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"time"

	"github.com/gaoLfun/dapi/internal/core"
)

type balancePath struct {
	path    string
	expires time.Time
}

func balancePathKey(upstream core.Upstream) [32]byte {
	// Credential changes must rediscover capabilities without retaining raw secrets.
	data, _ := json.Marshal([]string{upstream.BaseURL, upstream.Kind, upstream.APIKey, upstream.AccessToken, upstream.UserID, upstream.UserAgent})
	return sha256.Sum256(data)
}

func (p *Prober) preferredBalancePath(key [32]byte, now time.Time) string {
	p.balanceMu.Lock()
	defer p.balanceMu.Unlock()
	entry := p.balancePaths[key]
	if !now.Before(entry.expires) {
		delete(p.balancePaths, key)
		return ""
	}
	return entry.path
}

func (p *Prober) rememberBalancePath(key [32]byte, path string, now time.Time) {
	p.balanceMu.Lock()
	defer p.balanceMu.Unlock()
	if entry, ok := p.balancePaths[key]; ok && entry.path == path && now.Before(entry.expires) {
		return
	}
	if p.balancePaths == nil {
		p.balancePaths = make(map[[32]byte]balancePath)
	}
	for key, entry := range p.balancePaths {
		if !now.Before(entry.expires) {
			delete(p.balancePaths, key)
		}
	}
	if len(p.balancePaths) >= 1024 {
		return
	}
	p.balancePaths[key] = balancePath{path: path, expires: now.Add(time.Hour)}
}

func (p *Prober) forgetBalancePath(key [32]byte) {
	p.balanceMu.Lock()
	defer p.balanceMu.Unlock()
	delete(p.balancePaths, key)
}

func acquireProbe(ctx context.Context, slots chan struct{}) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if slots == nil {
		return nil
	}
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseProbe(slots chan struct{}) {
	if slots != nil {
		<-slots
	}
}
