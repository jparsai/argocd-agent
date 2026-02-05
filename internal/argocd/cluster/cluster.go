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

package cluster

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/argoproj-labs/argocd-agent/internal/event"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/sirupsen/logrus"

	appv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	cacheutil "github.com/argoproj/argo-cd/v3/util/cache"
	appstatecache "github.com/argoproj/argo-cd/v3/util/cache/appstate"
	"github.com/argoproj/argo-cd/v3/util/db"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const LabelKeySelfRegisteredCluster = "argocd-agent.argoproj-labs.io/self-registered-cluster"

// SetAgentConnectionStatus updates cluster info with connection state and time in mapped cluster at principal.
// This is called when the agent is connected or disconnected with the principal.
func (m *Manager) SetAgentConnectionStatus(agentName, status appv1.ConnectionStatus, modifiedAt time.Time) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Check if we have a mapping for the requested agent
	cluster := m.mapping(agentName)
	if cluster == nil {
		log().Errorf("Agent %s is not mapped to any cluster", agentName)
		return
	}

	state := "disconnected"
	if status == appv1.ConnectionStatusSuccessful {
		state = "connected"
	}

	// Update the cluster connection state and time in mapped cluster at principal.
	if err := m.setClusterInfo(cluster.Server, agentName, cluster.Name,
		&appv1.ClusterInfo{
			ConnectionState: appv1.ConnectionState{
				Status:     status,
				Message:    fmt.Sprintf("Agent: '%s' is %s with principal", agentName, state),
				ModifiedAt: &metav1.Time{Time: modifiedAt},
			},
		}); err != nil {
		log().Errorf("failed to refresh connection info in cluster: '%s' mapped with agent: '%s'. Error: %v", cluster.Name, agentName, err)
		return
	}

	log().Infof("Updated connection status to '%s' in Cluster: '%s' mapped with Agent: '%s'", status, cluster.Name, agentName)
}

// refreshClusterInfo gets latest cluster info from cache and re-saves it to avoid deletion of info
// by Argo CD after cache expiration time duration (i.e. 10 minutes)
func (m *Manager) refreshClusterInfo() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if m.clusterCache == nil {
		log().Warn("Cluster cache is not available, skipping refresh")
		return
	}

	// Iterate through all clusters.
	for agentName, cluster := range m.clusters {
		clusterInfo := &appv1.ClusterInfo{}

		if err := m.clusterCache.GetClusterInfo(cluster.Server, clusterInfo); err != nil {
			if !errors.Is(err, cacheutil.ErrCacheMiss) {
				log().Errorf("failed to get connection info from cluster: '%s' mapped with agent: '%s'. Error: %v", cluster.Name, agentName, err)
			}
			continue
		}

		// Re-save same info.
		if err := m.setClusterInfo(cluster.Server, agentName, cluster.Name, clusterInfo); err != nil {
			log().Errorf("failed to refresh connection info in cluster: '%s' mapped with agent: '%s'. Error: %v", cluster.Name, agentName, err)
			continue
		}
	}
}

// SetClusterCacheStats updates cluster cache info with Application, Resource and API counts in principal.
// This is called when principal receives clusterCacheInfoUpdate event from agent.
func (m *Manager) SetClusterCacheStats(clusterInfo *event.ClusterCacheInfo, agentName string) error {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	// Check if we have a mapping for the requested agent.
	cluster := m.mapping(agentName)
	if cluster == nil {
		log().Errorf("agent %s is not mapped to any cluster", agentName)
		return fmt.Errorf("agent %s is not mapped to any cluster", agentName)
	}

	existingClusterInfo := &appv1.ClusterInfo{}
	if m.clusterCache != nil {
		if err := m.clusterCache.GetClusterInfo(cluster.Server, existingClusterInfo); err != nil {
			if !errors.Is(err, cacheutil.ErrCacheMiss) {
				log().Errorf("failed to get existing cluster info for cluster: '%s' mapped with agent: '%s'. Error: %v", cluster.Name, agentName, err)
			}
		}
	}

	// Create new cluster info with cache stats
	newClusterInfo := &appv1.ClusterInfo{
		ApplicationsCount: clusterInfo.ApplicationsCount,
		CacheInfo: appv1.ClusterCacheInfo{
			APIsCount:      clusterInfo.APIsCount,
			ResourcesCount: clusterInfo.ResourcesCount,
		},
	}

	// Preserve existing cluster connection status
	if existingClusterInfo.ConnectionState != (appv1.ConnectionState{}) {
		newClusterInfo.ConnectionState = existingClusterInfo.ConnectionState
		if existingClusterInfo.CacheInfo.LastCacheSyncTime != nil {
			newClusterInfo.CacheInfo.LastCacheSyncTime = existingClusterInfo.CacheInfo.LastCacheSyncTime
		}
	}

	// Set the info in mapped cluster at principal.
	if err := m.setClusterInfo(cluster.Server, agentName, cluster.Name, newClusterInfo); err != nil {
		log().Errorf("failed to update cluster cache stats in cluster: '%s' mapped with agent: '%s'. Error: %v", cluster.Name, agentName, err)
		return err
	}

	log().WithFields(logrus.Fields{
		"applicationsCount": clusterInfo.ApplicationsCount,
		"apisCount":         clusterInfo.APIsCount,
		"resourcesCount":    clusterInfo.ResourcesCount,
		"cluster":           cluster.Name,
		"agent":             agentName,
	}).Infof("Updated cluster cache stats in cluster.")

	return nil
}

// setClusterInfo saves the given ClusterInfo in the cache.
func (m *Manager) setClusterInfo(clusterServer, agentName, clusterName string, clusterInfo *appv1.ClusterInfo) error {
	// Check if cluster cache is available
	if m.clusterCache == nil {
		return fmt.Errorf("cluster cache is not available")
	}

	// Save the given cluster info in cache.
	if err := m.clusterCache.SetClusterInfo(clusterServer, clusterInfo); err != nil {
		return fmt.Errorf("failed to refresh connection info in cluster: '%s' mapped with agent: '%s': %v", clusterName, agentName, err)
	}
	return nil
}

// NewClusterCacheInstance creates a new cache instance with Redis connection
func NewClusterCacheInstance(redisAddress, redisPassword string, redisCompressionType cacheutil.RedisCompressionType) (*appstatecache.Cache, error) {

	redisOptions := &redis.Options{
		Addr:     redisAddress,
		Password: redisPassword,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	}
	redisClient := redis.NewClient(redisOptions)

	clusterCache := appstatecache.NewCache(cacheutil.NewCache(
		cacheutil.NewRedisCache(redisClient, 0, redisCompressionType)), 0)

	return clusterCache, nil
}

// TokenIssuer is an interface for issuing resource proxy tokens.
// This is a subset of the issuer.Issuer interface to avoid circular dependencies.
type TokenIssuer interface {
	IssueResourceProxyToken(agentName string) (string, error)
}

// CreateClusterWithBearerToken creates a cluster secret for an agent using JWT bearer token authentication.
// The token is signed by the principal's signing key and used to authenticate to the resource proxy.
func CreateClusterWithBearerToken(ctx context.Context, kubeclient kubernetes.Interface, namespace, agentName, resourceProxyAddress string, tokenIssuer TokenIssuer, caCertPath string) error {
	logCtx := log().WithField("agent", agentName)
	logCtx.Debug("Creating cluster secret with bearer token authentication")

	// Generate bearer token for this agent
	bearerToken, err := tokenIssuer.IssueResourceProxyToken(agentName)
	if err != nil {
		logCtx.WithError(err).Error("Failed to issue resource proxy token")
		return fmt.Errorf("could not issue resource proxy token: %v", err)
	}
	logCtx.Debug("Successfully issued resource proxy token")

	// Read CA certificate for server verification
	caData, err := readCAFromFile(caCertPath)
	if err != nil {
		logCtx.WithError(err).Error("Failed to read CA certificate")
		return fmt.Errorf("could not read CA certificate: %v", err)
	}

	cluster := &appv1.Cluster{
		Server: fmt.Sprintf("https://%s?agentName=%s", resourceProxyAddress, agentName),
		Name:   agentName,
		Labels: map[string]string{
			LabelKeyClusterAgentMapping:   agentName,
			LabelKeySelfRegisteredCluster: "true",
		},
		Config: appv1.ClusterConfig{
			BearerToken: bearerToken,
			TLSClientConfig: appv1.TLSClientConfig{
				CAData: []byte(caData),
			},
		},
	}

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      getClusterSecretName(agentName),
			Namespace: namespace,
		},
	}
	if err := ClusterToSecret(cluster, secret); err != nil {
		logCtx.WithError(err).Error("Failed to convert cluster to secret")
		return fmt.Errorf("could not convert cluster to secret: %v", err)
	}

	if _, err = kubeclient.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			logCtx.Debug("Cluster secret already exists, skipping creation")
			return nil
		}
		logCtx.WithError(err).Error("Failed to create cluster secret")
		return fmt.Errorf("could not create cluster secret: %v", err)
	}

	logCtx.Info("Successfully created cluster secret with bearer token")
	return nil
}

// UpdateClusterBearerToken updates an existing cluster secret with a new bearer token.
// This is used when the signing key rotates and existing tokens become invalid.
func UpdateClusterBearerToken(ctx context.Context, kubeclient kubernetes.Interface, namespace, agentName string, tokenIssuer TokenIssuer) error {
	logCtx := log().WithField("agent", agentName)
	logCtx.Debug("Updating cluster secret bearer token")

	secretName := getClusterSecretName(agentName)

	secret, err := kubeclient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		logCtx.WithError(err).Error("Failed to get cluster secret")
		return fmt.Errorf("could not get cluster secret: %v", err)
	}

	cluster, err := db.SecretToCluster(secret)
	if err != nil {
		logCtx.WithError(err).Error("Failed to parse cluster secret")
		return fmt.Errorf("could not parse cluster secret: %v", err)
	}

	// Generate new bearer token
	bearerToken, err := tokenIssuer.IssueResourceProxyToken(agentName)
	if err != nil {
		logCtx.WithError(err).Error("Failed to issue new resource proxy token")
		return fmt.Errorf("could not issue resource proxy token: %v", err)
	}
	logCtx.Debug("Successfully issued new resource proxy token")

	cluster.Config.BearerToken = bearerToken

	if err := ClusterToSecret(cluster, secret); err != nil {
		logCtx.WithError(err).Error("Failed to convert cluster to secret")
		return fmt.Errorf("could not convert cluster to secret: %v", err)
	}

	if _, err = kubeclient.CoreV1().Secrets(namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
		logCtx.WithError(err).Error("Failed to update cluster secret")
		return fmt.Errorf("could not update cluster secret: %v", err)
	}

	logCtx.Info("Successfully updated cluster secret bearer token")
	return nil
}

// readCAFromFile reads the CA certificate from a file path.
// This allows mounting ConfigMaps or Secrets as volumes and pointing to the file.
func readCAFromFile(caCertPath string) (string, error) {
	if caCertPath == "" {
		return "", fmt.Errorf("CA certificate path is empty")
	}

	caData, err := os.ReadFile(caCertPath)
	if err != nil {
		return "", fmt.Errorf("could not read CA file %s: %v", caCertPath, err)
	}

	return string(caData), nil
}

// DeleteClusterSecret deletes the cluster secret for an agent.
func DeleteClusterSecret(ctx context.Context, kubeclient kubernetes.Interface, namespace, agentName string) error {
	logCtx := log().WithField("agent", agentName)
	logCtx.Debug("Deleting cluster secret")

	secretName := getClusterSecretName(agentName)
	err := kubeclient.CoreV1().Secrets(namespace).Delete(ctx, secretName, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		logCtx.WithError(err).Error("Failed to delete cluster secret")
		return fmt.Errorf("could not delete cluster secret: %v", err)
	}

	if apierrors.IsNotFound(err) {
		logCtx.Debug("Cluster secret not found, nothing to delete")
	} else {
		logCtx.Info("Successfully deleted cluster secret")
	}
	return nil
}

// GetClusterBearerToken retrieves the bearer token from an existing cluster secret.
func GetClusterBearerToken(ctx context.Context, kubeclient kubernetes.Interface, namespace, agentName string) (string, error) {
	logCtx := log().WithField("agent", agentName)
	logCtx.Debug("Retrieving bearer token from cluster secret")

	secretName := getClusterSecretName(agentName)
	secret, err := kubeclient.CoreV1().Secrets(namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		logCtx.WithError(err).Debug("Failed to get cluster secret")
		return "", fmt.Errorf("could not get cluster secret: %v", err)
	}

	cluster, err := db.SecretToCluster(secret)
	if err != nil {
		logCtx.WithError(err).Error("Failed to parse cluster secret")
		return "", fmt.Errorf("could not parse cluster secret: %v", err)
	}

	return cluster.Config.BearerToken, nil
}

// ClusterSecretExists checks if a cluster secret exists for the given agent.
func ClusterSecretExists(ctx context.Context, kubeclient kubernetes.Interface, namespace, agentName string) (bool, error) {
	if _, err := kubeclient.CoreV1().Secrets(namespace).Get(ctx, getClusterSecretName(agentName), metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func getClusterSecretName(agentName string) string {
	return "cluster-" + agentName
}
