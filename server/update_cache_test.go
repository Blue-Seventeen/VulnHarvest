package server

import (
	"context"
	"errors"
	"github.com/Autumn-27/artex/selfupdate"
	"net/http"
	"sync"
	"time"
)

// Legacy cache helper retained only for compatibility tests, never linked into production.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch 是取数函数，仅为测试留的注入点；为 nil 时走真正的 GitHub 查询。
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// 失败结果也缓存一小会儿，否则 GitHub 不可达时每次页面加载都要干等一次超时；
	// 但 TTL 要短，网络恢复后很快就能自己好。
	releaseErrTTL = 2 * time.Minute
	// 查询用的超时。NewClient 的 30 分钟超时是给下载整包用的，查版本不能等那么久。
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get 返回最新 Release，命中缓存则不访问网络。
//
// 取数期间一直持有锁：并发请求会排队等同一次查询的结果，而不是各自去打 GitHub
// （页面刚加载时多个标签页同时来查，正是最容易触发限流的时刻）。
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// 请求被取消（用户关了标签页）不代表 GitHub 有问题，别把它写进缓存，
	// 否则下一个访客会拿到一条莫名其妙的"已取消"错误。
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}
