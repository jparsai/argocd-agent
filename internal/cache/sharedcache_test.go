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
	"testing"
	"time"

	"github.com/argoproj/argo-cd/v3/common"
	"github.com/argoproj/argo-cd/v3/util/cache"
	appstatecache "github.com/argoproj/argo-cd/v3/util/cache/appstate"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func setup(t *testing.T, namespace string) *fake.Clientset {
	kubeclient := fake.NewSimpleClientset()

	// Create the Redis secret that the cache expects
	redisSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      common.RedisInitialCredentials,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			common.RedisInitialCredentialsKey: []byte("test-password"),
		},
		Type: corev1.SecretTypeOpaque,
	}

	_, err := kubeclient.CoreV1().Secrets(namespace).Create(context.Background(), redisSecret, metav1.CreateOptions{})
	require.NoError(t, err)

	return kubeclient
}

func Test_GetAgentCacheInstance(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)
	redisAddress := "localhost:6379"
	redisCompressionType := cache.RedisCompressionGZip

	t.Run("Create new agent cache instance", func(t *testing.T) {
		sharedCache = &SharedCache{}

		cacheInstance, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)

		require.NoError(t, err)
		require.NotNil(t, cacheInstance)
		require.NotNil(t, sharedCache.agentCacheInstance)
		require.Equal(t, sharedCache.agentCacheInstance, cacheInstance)
		require.False(t, sharedCache.agentCacheInstanceCreatedAt.IsZero())
	})

	t.Run("Return existing agent cache instance when not expired", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Create first cache instance
		cacheInstance1, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
		require.NoError(t, err)
		require.NotNil(t, cacheInstance1)

		// Get second cache instance, but it should return the first instance
		cacheInstance2, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
		require.NoError(t, err)
		require.Equal(t, cacheInstance1, cacheInstance2)
		require.Equal(t, sharedCache.agentCacheInstance, cacheInstance2)
	})

	t.Run("Create new agent cache instance when expired", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Create first cache instance
		cacheInstance1, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
		require.NoError(t, err)
		require.NotNil(t, cacheInstance1)

		// Expire the cache by setting creation time to past
		sharedCache.agentCacheInstanceCreatedAt = time.Now().Add(-cacheInstanceExpirationDuration - time.Minute)

		// Get second cache instance, it should create a new one
		cacheInstance2, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, redisCompressionType)
		require.NoError(t, err)
		require.NotEqual(t, cacheInstance1, cacheInstance2)
		require.Equal(t, sharedCache.agentCacheInstance, cacheInstance2)
	})
}

func Test_GetPrincipalCacheInstance(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)
	redisAddress := "localhost:6379"

	t.Run("Create new principal cache instance", func(t *testing.T) {
		sharedCache = &SharedCache{}

		cacheInstance, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)

		require.NoError(t, err)
		require.NotNil(t, cacheInstance)
		require.NotNil(t, sharedCache.principalCacheInstance)
		require.Equal(t, sharedCache.principalCacheInstance, cacheInstance)
		require.False(t, sharedCache.principalCacheInstanceCreatedAt.IsZero())
	})

	t.Run("Return existing principal cache instance when not expired", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Create first cache instance
		cacheInstance1, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, cacheInstance1)

		// Get second cache instance, it should return the first instance
		cacheInstance2, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.Equal(t, cacheInstance1, cacheInstance2)
		require.Equal(t, sharedCache.principalCacheInstance, cacheInstance2)
	})

	t.Run("Create new principal cache instance when expired", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Create first cache instance
		cacheInstance1, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, cacheInstance1)

		// Expire the cache by setting creation time to past
		sharedCache.principalCacheInstanceCreatedAt = time.Now().Add(-cacheInstanceExpirationDuration - time.Minute)

		// Get second cache instance, it should create a new one
		cacheInstance2, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotEqual(t, cacheInstance1, cacheInstance2)
		require.Equal(t, sharedCache.principalCacheInstance, cacheInstance2)
	})
}

func Test_CreateCacheInstance(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)

	t.Run("Create cache instance", func(t *testing.T) {
		redisAddress := "localhost:6379"

		cacheInstance, err := sharedCache.createCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)

		require.NoError(t, err)
		require.NotNil(t, cacheInstance)
		require.IsType(t, &appstatecache.Cache{}, cacheInstance)
	})
}

func Test_GetCacheInstance(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)
	redisAddress := "localhost:6379"

	t.Run("Get agent cache instance", func(t *testing.T) {
		sharedCache = &SharedCache{}

		cacheInstance, err := GetCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip, SourceAgent)

		require.NoError(t, err)
		require.NotNil(t, cacheInstance)
		require.NotNil(t, sharedCache.agentCacheInstance)
		require.Equal(t, sharedCache.agentCacheInstance, cacheInstance)
	})

	t.Run("Get principal cache instance", func(t *testing.T) {
		sharedCache = &SharedCache{}

		cacheInstance, err := GetCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip, SourcePrincipal)

		require.NoError(t, err)
		require.NotNil(t, cacheInstance)
		require.NotNil(t, sharedCache.principalCacheInstance)
		require.Equal(t, sharedCache.principalCacheInstance, cacheInstance)
	})

	t.Run("Get cache instance with unknown source", func(t *testing.T) {
		sharedCache = &SharedCache{}

		cacheInstance, err := GetCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip, "unknown-source")

		require.Error(t, err)
		require.Nil(t, cacheInstance)
		require.Contains(t, err.Error(), "unknown source")
	})
}

func Test_Isolation(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)
	redisAddress := "localhost:6379"

	t.Run("Agent and principal caches are isolated", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Get agent cache
		agentCache, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, agentCache)

		// Get principal cache
		principalCache, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, principalCache)

		// They should be different instances
		require.NotEqual(t, agentCache, principalCache)
		require.Equal(t, sharedCache.agentCacheInstance, agentCache)
		require.Equal(t, sharedCache.principalCacheInstance, principalCache)
	})
}

func Test_ResetCache(t *testing.T) {
	ctx := context.Background()
	namespace := "test-namespace"
	kubeclient := setup(t, namespace)
	redisAddress := "localhost:6379"

	t.Run("ResetForTesting clears all cache instances", func(t *testing.T) {
		sharedCache = &SharedCache{}

		// Create cache instances
		agentCache, err := sharedCache.getAgentCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, agentCache)

		principalCache, err := sharedCache.getPrincipalCacheInstance(ctx, kubeclient, namespace, redisAddress, cache.RedisCompressionGZip)
		require.NoError(t, err)
		require.NotNil(t, principalCache)

		// Verify instances are set
		require.NotNil(t, sharedCache.agentCacheInstance)
		require.NotNil(t, sharedCache.principalCacheInstance)
		require.False(t, sharedCache.agentCacheInstanceCreatedAt.IsZero())
		require.False(t, sharedCache.principalCacheInstanceCreatedAt.IsZero())

		// Reset cache
		ResetForTesting()

		// Verify instances are cleared
		require.Nil(t, sharedCache.agentCacheInstance)
		require.Nil(t, sharedCache.principalCacheInstance)
		require.True(t, sharedCache.agentCacheInstanceCreatedAt.IsZero())
		require.True(t, sharedCache.principalCacheInstanceCreatedAt.IsZero())
	})
}
