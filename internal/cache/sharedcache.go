// Copyright 2025 The argocd-agent Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cache

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/argoproj/argo-cd/v3/common"
	cacheutil "github.com/argoproj/argo-cd/v3/util/cache"
	appstatecache "github.com/argoproj/argo-cd/v3/util/cache/appstate"
	"github.com/redis/go-redis/v9"
	"k8s.io/client-go/kubernetes"
)

const (
	// cacheExpirationDuration is the duration after which the cache instance is expired.
	cacheInstanceExpirationDuration = 10 * time.Minute

	SourceAgent     = "agent"
	SourcePrincipal = "principal"
)

type SharedCache struct {
	mu sync.RWMutex

	agentCacheInstance          *appstatecache.Cache
	agentCacheInstanceCreatedAt time.Time

	principalCacheInstance          *appstatecache.Cache
	principalCacheInstanceCreatedAt time.Time
}

// Initialize instance of SharedCache.
var sharedCache = &SharedCache{}

// getAgentCacheInstance returns the agent cache instance
func (sc *SharedCache) getAgentCacheInstance(ctx context.Context, kubeclient kubernetes.Interface,
	namespace, redisAddress string, redisCompressionType cacheutil.RedisCompressionType) (*appstatecache.Cache, error) {

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Check if cache exists and not expired, then return existing cache instance else create a new one.
	if sc.agentCacheInstance != nil {
		if time.Since(sc.agentCacheInstanceCreatedAt) < cacheInstanceExpirationDuration {
			return sc.agentCacheInstance, nil
		}
		sc.agentCacheInstance = nil
	}

	sc.agentCacheInstanceCreatedAt = time.Now()

	cache, err := sc.createCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
	if err != nil {
		return nil, err
	}

	sc.agentCacheInstance = cache
	return sc.agentCacheInstance, nil
}

// getPrincipalCacheInstance returns the principal cache instance
func (sc *SharedCache) getPrincipalCacheInstance(ctx context.Context, kubeclient kubernetes.Interface,
	namespace, redisAddress string, redisCompressionType cacheutil.RedisCompressionType) (*appstatecache.Cache, error) {

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Check if cache exists and not expired, then return existing cache instance else create a new one.
	if sc.principalCacheInstance != nil {
		if time.Since(sc.principalCacheInstanceCreatedAt) < cacheInstanceExpirationDuration {
			return sc.principalCacheInstance, nil
		}
		sc.principalCacheInstance = nil
	}

	sc.principalCacheInstanceCreatedAt = time.Now()

	cache, err := sc.createCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
	if err != nil {
		return nil, err
	}

	sc.principalCacheInstance = cache
	return sc.principalCacheInstance, nil
}

// createCacheInstance creates a new cache instance with Redis connection
func (sc *SharedCache) createCacheInstance(ctx context.Context, kubeclient kubernetes.Interface, namespace, redisAddress string,
	redisCompressionType cacheutil.RedisCompressionType) (*appstatecache.Cache, error) {
	redisOptions := &redis.Options{Addr: redisAddress}

	if err := common.SetOptionalRedisPasswordFromKubeConfig(ctx, kubeclient, namespace, redisOptions); err != nil {
		return nil, fmt.Errorf("failed to set redis password for namespace %s: %v", namespace, err)
	}

	redisClient := redis.NewClient(redisOptions)

	clusterCache := appstatecache.NewCache(cacheutil.NewCache(
		cacheutil.NewRedisCache(redisClient, cacheInstanceExpirationDuration, redisCompressionType),
	), cacheInstanceExpirationDuration)

	return clusterCache, nil
}

// GetCacheInstance returns the cache instance for the given source
func GetCacheInstance(ctx context.Context, kubeclient kubernetes.Interface, namespace, redisAddress string,
	redisCompressionType cacheutil.RedisCompressionType, source string) (*appstatecache.Cache, error) {

	switch source {
	case SourceAgent:
		return sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
	case SourcePrincipal:
		return sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
	default:
		return nil, fmt.Errorf("unknown source: %s", source)
	}
}

// ResetForTesting resets the shared cache for testing purposes only.
// It is needed because we close the redis connection after each test,
// hence the shared cache is not valid anymore.
func ResetForTesting() {
	sharedCache.mu.Lock()
	defer sharedCache.mu.Unlock()
	sharedCache.agentCacheInstance = nil
	sharedCache.principalCacheInstance = nil
	sharedCache.agentCacheInstanceCreatedAt = time.Time{}
	sharedCache.principalCacheInstanceCreatedAt = time.Time{}
}
