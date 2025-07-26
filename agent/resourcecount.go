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

package agent

import (
	"context"
	"fmt"
	"time"

	appv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	appstatecache "github.com/argoproj/argo-cd/v3/util/cache/appstate"
	"github.com/sirupsen/logrus"

	"github.com/argoproj-labs/argocd-agent/internal/event"
	"github.com/argoproj-labs/argocd-agent/pkg/types"
	cacheutil "github.com/argoproj/argo-cd/v3/util/cache"
)

const clusterInfoWatcherInterval = 1 * time.Minute

// startPeriodicClusterInfoSync starts a background goroutine that periodically fetches cluster information,
// having application, api and resource counts, from the cluster and sends this infor to principal over gRPC
func (a *Agent) startPeriodicClusterInfoSync(ctx context.Context) {

	if a.mode != types.AgentModeManaged {
		return
	}

	logCtx := log().WithFields(logrus.Fields{
		"method": "startPeriodicClusterInfoSync",
	})

	logCtx.Info("Starting cluster info count watcher")

	go func() {
		ticker := time.NewTicker(clusterInfoWatcherInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logCtx.Info("cluster info watcher stopped")
				return
			case <-ticker.C:
				if err := a.refreshClusterCacheInfo(); err != nil {
					logCtx.WithError(err).Error("Failed to get cluster info")
				}
			}
		}
	}()
}

func (a *Agent) refreshClusterCacheInfo() error {
	logCtx := log().WithFields(logrus.Fields{
		"method": "refreshClusterCacheInfo",
	})

	cache, err := a.getRedisCache()
	if err != nil {
		logCtx.WithError(err).Error("Failed to get redis cache")
		return err
	}

	// TODO: get cluster server from agent config
	clusterServer := "https://kubernetes.default.svc"

	// Fetch cluster info from redis cache
	clusterInfo := &appv1.ClusterInfo{}
	if err := cache.GetClusterInfo(clusterServer, clusterInfo); err != nil {
		logCtx.WithError(err).Error("Failed to get cluster info from cache")
		return err
	}

	// Send ClusterInfo event to principal
	sendQ := a.queues.SendQ(defaultQueueName)

	if sendQ != nil {
		clusterInfoEvent := a.emitter.ClusterInfoEvent(event.ClusterInfoUpdate, clusterInfo)
		sendQ.Add(clusterInfoEvent)
		logCtx.WithFields(logrus.Fields{
			"applicationsCount": clusterInfo.ApplicationsCount,
			"apisCount":         clusterInfo.CacheInfo.APIsCount,
			"resourcesCount":    clusterInfo.CacheInfo.ResourcesCount,
		}).Info("Sent ClusterInfo event to principal")
	} else {
		logCtx.Error("Default queue not found, unable to send ClusterInfo event")
	}

	return nil
}

func (a *Agent) getRedisCache() (*appstatecache.Cache, error) {
	redisClient, _, err := a.getRedisClientAndCache()
	if err != nil {
		return nil, fmt.Errorf("failed to get redis client: %w", err)
	}

	cache := appstatecache.NewCache(cacheutil.NewCache(
		cacheutil.NewRedisCache(redisClient, time.Minute, cacheutil.RedisCompressionGZip)), time.Minute)
	return cache, nil
}
