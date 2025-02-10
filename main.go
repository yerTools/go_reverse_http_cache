package main

import (
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/valyala/fasthttp"
	"golang.org/x/sync/singleflight"

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

type addToCacheLevel int

const (
	dontAddToCache addToCacheLevel = iota
	addToCacheIfNotCached
	forceAddToCache
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
	releaseResponseMap := make(map[releaseResponseLevel][]cacheValue)
	nextReleaseResponseMap := make(map[releaseResponseLevel][]cacheValue)
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
							if response.Response != nil {
								fasthttp.ReleaseResponse(response.Response)
							}
							if response.Request != nil {
								fasthttp.ReleaseRequest(response.Request)
							}
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

		releaseResponseMap[releaseResponseLevel0] = append(releaseResponseMap[releaseResponseLevel0], value.Value)
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
		lastCostMutex:      sync.RWMutex{},
	}

	for i := 0; i < availableForwarderSize; i++ {
		c.availableForwarder <- struct{}{}
	}

	go func() {
		refreshTicker := time.NewTicker(time.Second * 5)

		for now := range refreshTicker.C {
			wg := sync.WaitGroup{}

			for _, value := range c.cache.ValuesInExpirationBuckets(nil, time.Time{}, now.Add(time.Second*15)) {
				if value.Value.Request == nil {
					continue
				}
				wg.Add(1)
				go func() {
					defer wg.Done()
					resp := fasthttp.AcquireResponse()
					defer fasthttp.ReleaseResponse(resp)
					c.forwardHandler(value.Value.Request, resp, value.Key, forceAddToCache)
				}()
			}

			wg.Wait()
		}
	}()

	log.Println("fasthttp server is running on :8161")
	if err := fasthttp.ListenAndServe(":8161", c.requestHandler); err != nil {
		log.Fatalf("Could not start fasthttp server: %v", err)
	}
}

type cacheValue struct {
	Request  *fasthttp.Request
	Response *fasthttp.Response
}

type httpCache struct {
	cache  *cache.Cache[cacheValue]
	client *fasthttp.Client

	availableForwarder chan struct{}

	sfGroup singleflight.Group

	lastCost        int64
	lastCostExpires time.Time
	lastCostMutex   sync.RWMutex
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

func (c *httpCache) forwardHandler(targetReq *fasthttp.Request, targetResp *fasthttp.Response, key cache.StoreKey, addToCache addToCacheLevel) error {
	if addToCache == dontAddToCache {
		<-c.availableForwarder
		defer func() { c.availableForwarder <- struct{}{} }()

		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		targetReq.CopyTo(req)
		req.SetHost("ltl.re")
		req.URI().SetScheme("https")
		log.Printf("Forwarding to %v\n", req.URI())

		err := c.client.Do(req, targetResp)
		if err != nil {
			log.Printf("Error forwarding request: %v\n", err)
			c.writeError(targetResp, "Internal Cache Server Error", fasthttp.StatusInternalServerError)
			return err
		}
		targetResp.Header.Set("X-Cache-Status", "miss")
		targetResp.Header.Set("X-Cache-Cacheable", "false")
		return nil
	}

	res, err, shared := c.sfGroup.Do(key.String(), func() (interface{}, error) {
		<-c.availableForwarder
		defer func() { c.availableForwarder <- struct{}{} }()

		if addToCache == addToCacheIfNotCached {
			cached, ok := c.cache.Get(key)
			if ok {
				cached.Value.Response.CopyTo(targetResp)

				targetResp.Header.Set("X-Cache-Allocation", strconv.FormatInt(c.cacheCost(), 10))

				return cached.Value, nil
			}
		}

		req := fasthttp.AcquireRequest()
		defer fasthttp.ReleaseRequest(req)
		targetReq.CopyTo(req)
		req.SetHost("ltl.re")
		req.URI().SetScheme("https")
		log.Printf("Forwarding to %v (singleflight)\n", req.URI())

		resp := fasthttp.AcquireResponse()
		err := c.client.Do(req, resp)
		if err != nil {
			fasthttp.ReleaseResponse(resp)
			return nil, err
		}

		reqCopy := fasthttp.AcquireRequest()
		req.CopyTo(reqCopy)

		cost := int64(8+8) + int64(len(resp.Body())+len(reqCopy.Body()))
		for _, hKey := range resp.Header.PeekKeys() {
			cost += int64(len(hKey))
			for _, vv := range resp.Header.PeekAll(string(hKey)) {
				cost += int64(len(vv))
			}
		}
		for _, hKey := range reqCopy.Header.PeekKeys() {
			cost += int64(len(hKey))
			for _, vv := range reqCopy.Header.PeekAll(string(hKey)) {
				cost += int64(len(vv))
			}
		}

		cached := c.cache.Set(key, cacheValue{Response: resp, Request: reqCopy}, cost, 60*time.Second)
		if cached == nil {
			return resp, nil
		}

		resp.Header.Set("X-Cache-Cacheable", "true")
		resp.Header.Set("X-Cache-Status", "hit")
		resp.Header.Set("X-Cache-Key", key.Hex())
		resp.Header.Set("X-Cache-Cost", strconv.FormatInt(cost, 10))
		resp.Header.Set("X-Cache-Expiration", cached.Expiration.Format(time.RFC3339Nano))

		if len(resp.Header.Peek(fasthttp.HeaderCacheControl)) == 0 {
			resp.Header.Set(fasthttp.HeaderCacheControl, "public")
		}
		if len(resp.Header.Peek(fasthttp.HeaderExpires)) == 0 {
			resp.Header.Set(fasthttp.HeaderExpires, cached.Expiration.Format(http.TimeFormat))
		}
		if len(resp.Header.Peek("CDN-Cache-Control")) == 0 {
			resp.Header.Set("CDN-Cache-Control", "max-age=60")
		}
		if len(resp.Header.Peek(fasthttp.HeaderConnection)) == 0 {
			resp.Header.Set(fasthttp.HeaderConnection, "keep-alive")
		}

		return resp, nil
	})

	if err != nil {
		log.Printf("Error forwarding request (singleflight): %v\n", err)
		c.writeError(targetResp, "Internal Cache Server Error", fasthttp.StatusInternalServerError)
		return err
	}

	resp, ok := res.(*fasthttp.Response)
	if !ok {
		resp = res.(cacheValue).Response
	}
	resp.CopyTo(targetResp)
	if !shared && ok {
		targetResp.Header.Set("X-Cache-Status", "miss")
	}

	return nil
}

func (*httpCache) writeError(resp *fasthttp.Response, msg string, statusCode int) {
	resp.Reset()
	resp.SetStatusCode(statusCode)
	resp.Header.SetContentTypeBytes([]byte("text/plain; charset=utf-8"))
	resp.SetBodyString(msg)
}

func (c *httpCache) requestHandler(ctx *fasthttp.RequestCtx) {
	//log.Printf("ServeHTTP: %v\n", ctx.Request.URI())

	//log.Println("Calculating key")
	key, ok := calculateKey(&ctx.Request)
	if !ok {
		c.forwardHandler(&ctx.Request, &ctx.Response, key, dontAddToCache)
		return
	}
	//log.Printf("Key: %d\n", key)

	cached, ok := c.cache.Get(key)
	if ok {
		//log.Println("Cache hit")

		cached.Value.Response.CopyTo(&ctx.Response)

		ctx.Response.Header.Set("X-Cache-Allocation", strconv.FormatInt(c.cacheCost(), 10))

		return
	}

	//log.Println("Cache miss, calling forward handler")
	c.forwardHandler(&ctx.Request, &ctx.Response, key, addToCacheIfNotCached)
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
