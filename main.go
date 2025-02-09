package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"sync"
	"time"

	"github.com/valyala/fasthttp"

	"github.com/yerTools/go_reverse_http_cache/src/go/cache"
)

const availableForwarderSize = 16

type releaseResponseLevel int

const (
	releaseResponseLevel0 releaseResponseLevel = iota
	releaseResponseLevel1
	releaseResponseLevel2
	releaseResponseLevel3
)

func nextReleaseResponseLevel(level releaseResponseLevel) (releaseResponseLevel, bool) {
	switch level {
	case releaseResponseLevel0:
		return releaseResponseLevel1, false
	case releaseResponseLevel1:
		return releaseResponseLevel2, false
	case releaseResponseLevel2:
		return releaseResponseLevel3, false
	case releaseResponseLevel3:
		return level, true
	default:
		return level, true
	}
}

func main() {
	go func() {
		log.Println(http.ListenAndServe("localhost:6060", nil))
	}()

	releaseResponseMap := make(map[releaseResponseLevel][]*fasthttp.Response)
	nextReleaseResponseMap := make(map[releaseResponseLevel][]*fasthttp.Response)
	releaseResponseMapLock := sync.Mutex{}

	releaseResponseTicker := time.NewTicker(time.Second)
	go func() {
		for range releaseResponseTicker.C {
			func() {
				for rrl := range nextReleaseResponseMap {
					delete(nextReleaseResponseMap, rrl)
				}

				releaseResponseMapLock.Lock()
				defer releaseResponseMapLock.Unlock()

				for level, responses := range releaseResponseMap {
					nextLevel, release := nextReleaseResponseLevel(level)
					if release {
						for _, response := range responses {
							fasthttp.ReleaseResponse(response)
						}
						nextReleaseResponseMap[releaseResponseLevel0] = responses[:0]
					} else {
						nextReleaseResponseMap[nextLevel] = responses
					}
				}

				releaseResponseMap, nextReleaseResponseMap = nextReleaseResponseMap, releaseResponseMap
			}()
		}
	}()

	//log.Println("Creating cache")
	cache := cache.NewCache[cacheValue](time.Millisecond*500, func(value *cache.StoreItem[cacheValue]) {
		//log.Printf("Removing %v\n", value.Key)
		releaseResponseMapLock.Lock()
		defer releaseResponseMapLock.Unlock()

		releaseResponseMap[releaseResponseLevel0] = append(releaseResponseMap[releaseResponseLevel0], value.Value.Resp)
	})

	c := &httpCache{
		cache: cache,
		client: &fasthttp.Client{
			ReadTimeout:                   30 * time.Second,
			WriteTimeout:                  30 * time.Second,
			MaxIdleConnDuration:           60 * time.Second,
			NoDefaultUserAgentHeader:      true,
			DisableHeaderNamesNormalizing: false,
			DisablePathNormalizing:        false,
			Dial: (&fasthttp.TCPDialer{
				Concurrency:      availableForwarderSize * 2,
				DNSCacheDuration: time.Hour,
			}).Dial,
		},
		availableForwarder: make(chan struct{}, availableForwarderSize),
		lastCost:           0,
		lastCostMutex:      &sync.RWMutex{},
	}

	for i := 0; i < availableForwarderSize; i++ {
		c.availableForwarder <- struct{}{}
	}

	log.Println("fasthttp server is running on :8161")
	if err := fasthttp.ListenAndServe(":8161", c.requestHandler); err != nil {
		log.Fatalf("Could not start fasthttp server: %v", err)
	}
}

type cacheValue struct {
	Resp *fasthttp.Response
}

type httpCache struct {
	cache              *cache.Cache[cacheValue]
	client             *fasthttp.Client
	availableForwarder chan struct{}
	lastCost           int64
	lastCostExpires    time.Time
	lastCostMutex      *sync.RWMutex
}

func (c *httpCache) cacheCost() int64 {
	now := time.Now()
	c.lastCostMutex.RLock()

	if now.Before(c.lastCostExpires) {
		defer c.lastCostMutex.RUnlock()
		return c.lastCost
	}
	c.lastCostMutex.RUnlock()

	c.lastCostMutex.Lock()
	defer c.lastCostMutex.Unlock()

	c.lastCost = c.cache.Cost()
	c.lastCostExpires = now.Add(time.Second)

	return c.lastCost
}

func (c *httpCache) forwardHandler(ctx *fasthttp.RequestCtx, key cache.StoreKey, addToCache bool) {
	//log.Printf("Requesting for %v\n", ctx.Request.URI())

	<-c.availableForwarder
	defer func() {
		c.availableForwarder <- struct{}{}
	}()

	//log.Println("Got forwarding slot")

	if addToCache {
		cached, ok := c.cache.Get(key)
		if ok {
			//log.Println("Don't need to forward: cache hit")

			cached.Value.Resp.CopyTo(&ctx.Response)

			ctx.Response.Header.Set("X-Cache-Allocation", strconv.FormatInt(c.cacheCost(), 10))

			return
		}
	}

	req := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(req)
	ctx.Request.CopyTo(req)
	req.SetHost("ltl.re")
	req.URI().SetScheme("https")
	log.Printf("Forwarding to %v\n", req.URI())

	if !addToCache {
		c.client.Do(req, &ctx.Response)

		ctx.Response.Header.Set("X-Cache-Status", "miss")
		ctx.Response.Header.Set("X-Cache-Cacheable", "false")
		return
	}

	resp := fasthttp.AcquireResponse()
	err := c.client.Do(req, resp)
	if err != nil {
		log.Printf("Error forwarding request: %v\n", err)
		ctx.Error("Internal Cache Server Error", fasthttp.StatusInternalServerError)
		return
	}

	cost := int64(8) + int64(len(resp.Body()))
	for _, key := range resp.Header.PeekKeys() {
		cost += int64(len(key))
		for _, vv := range resp.Header.PeekAll(string(key)) {
			cost += int64(len(vv))
		}
	}

	//log.Println("Setting cache")
	cached := c.cache.Set(key, cacheValue{Resp: resp}, cost, 60*time.Second)
	if cached == nil {
		//log.Println("Could not set cache")
		resp.CopyTo(&ctx.Response)
		return
	}

	resp.Header.Set("X-Cache-Cacheable", "true")
	resp.Header.Set("X-Cache-Status", "hit")
	resp.Header.Set("X-Cache-Key", fmt.Sprintf("%d:%d", key.Key, key.Conflict))
	resp.Header.Set("X-Cache-Cost", strconv.FormatInt(cost, 10))
	resp.Header.Set("X-Cache-Expiration", cached.Expiration.Format(time.RFC3339Nano))
	if len(resp.Header.Peek("Cache-Control")) == 0 {
		resp.Header.Set("Cache-Control", "public, immutable")
	}
	if len(resp.Header.Peek("Expires")) == 0 {
		resp.Header.Set("Expires", cached.Expiration.Format(http.TimeFormat))
	}
	if len(resp.Header.Peek("Connection")) == 0 {
		resp.Header.Set("Connection", "keep-alive")
	}

	resp.CopyTo(&ctx.Response)

	ctx.Response.Header.Set("X-Cache-Status", "miss")
}

func (c *httpCache) requestHandler(ctx *fasthttp.RequestCtx) {
	//log.Printf("ServeHTTP: %v\n", ctx.Request.URI())

	//log.Println("Calculating key")
	key, ok := calculateKey(&ctx.Request)
	if !ok {
		c.forwardHandler(ctx, key, false)
		return
	}
	//log.Printf("Key: %d\n", key)

	cached, ok := c.cache.Get(key)
	if ok {
		//log.Println("Cache hit")

		cached.Value.Resp.CopyTo(&ctx.Response)

		ctx.Response.Header.Set("X-Cache-Allocation", strconv.FormatInt(c.cacheCost(), 10))

		return
	}

	//log.Println("Cache miss, calling forward handler")
	c.forwardHandler(ctx, key, true)
}

func writeSection(h *cache.StoreKeyHash, section []byte, delimiter []byte) {
	h.Write(delimiter)
	if section == nil {
		return
	}

	h.Write(section)
}

func calculateKey(r *fasthttp.Request) (cache.StoreKey, bool) {

	method := r.Header.Method()
	methodStr := string(method)

	if methodStr != "HEAD" && methodStr != "GET" {
		return cache.StoreKey{}, false
	}

	hasher := cache.NewStoreKeyHash(161_269_1337, 1337_269_161)
	delimiter := []byte{161, 2, 6, 9, 0, 13, 10, 13, 10, 0}

	writeSection(hasher, method, delimiter)

	writeSection(hasher, r.Header.Peek(fasthttp.HeaderContentRange), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderRange), delimiter)

	writeSection(hasher, r.Header.Peek(fasthttp.HeaderAuthorization), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderProxyAuthorization), delimiter)

	writeSection(hasher, r.Header.Host(), delimiter)
	writeSection(hasher, r.URI().Path(), delimiter)
	writeSection(hasher, r.URI().QueryString(), delimiter)

	writeSection(hasher, r.Header.Peek(fasthttp.HeaderAccept), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderAcceptEncoding), delimiter)

	writeSection(hasher, r.Header.Peek(fasthttp.HeaderIfMatch), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderIfModifiedSince), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderIfNoneMatch), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderIfRange), delimiter)
	writeSection(hasher, r.Header.Peek(fasthttp.HeaderIfUnmodifiedSince), delimiter)

	return hasher.StoreKey(), true
}
