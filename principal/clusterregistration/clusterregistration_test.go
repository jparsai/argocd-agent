// Copyright 2026 The argocd-agent Authors
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

package clusterregistration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/argoproj-labs/argocd-agent/internal/argocd/cluster"
	"github.com/argoproj-labs/argocd-agent/internal/config"
	issuermocks "github.com/argoproj-labs/argocd-agent/internal/issuer/mocks"
	"github.com/argoproj-labs/argocd-agent/internal/tlsutil"
	"github.com/argoproj-labs/argocd-agent/test/fake/kube"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testNamespace         = "argocd"
	testResourceProxyAddr = "resource-proxy.argocd.svc:8443"
	testAgentName         = "test-agent"
)

func getClusterSecretName(agentName string) string {
	return "cluster-" + agentName
}

func createTestCACertFile(t *testing.T) string {
	t.Helper()
	caCertPEM, _, err := tlsutil.GenerateCaCertificate(config.SecretNamePrincipalCA)
	require.NoError(t, err, "generate CA certificate")

	tmpDir := t.TempDir()
	caPath := filepath.Join(tmpDir, "ca.crt")
	err = os.WriteFile(caPath, []byte(caCertPEM), 0600)
	require.NoError(t, err, "write CA cert file")

	return caPath
}

func createMockIssuer(t *testing.T) *issuermocks.Issuer {
	iss := issuermocks.NewIssuer(t)
	iss.On("IssueResourceProxyToken", mock.Anything).Return("test-token", nil).Maybe()
	iss.On("ValidateResourceProxyToken", mock.Anything).Return(nil, nil).Maybe()
	return iss
}

func Test_NewClusterRegistrationManager(t *testing.T) {
	t.Run("Create manager with self cluster registration enabled", func(t *testing.T) {
		kubeclient := kube.NewFakeKubeClient(testNamespace)
		iss := createMockIssuer(t)
		caCertPath := createTestCACertFile(t)
		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		require.NotNil(t, mgr)
		assert.True(t, mgr.selfClusterRegistrationEnabled)
		assert.Equal(t, testNamespace, mgr.namespace)
		assert.Equal(t, testResourceProxyAddr, mgr.resourceProxyAddress)
		assert.NotNil(t, mgr.kubeclient)
	})

	t.Run("Create manager with self cluster registration disabled", func(t *testing.T) {
		kubeclient := kube.NewFakeKubeClient(testNamespace)
		iss := createMockIssuer(t)
		mgr := NewClusterRegistrationManager(false, testNamespace, testResourceProxyAddr, "", kubeclient, iss)

		require.NotNil(t, mgr)
		assert.False(t, mgr.selfClusterRegistrationEnabled)
		assert.Equal(t, testNamespace, mgr.namespace)
		assert.Equal(t, testResourceProxyAddr, mgr.resourceProxyAddress)
	})
}

func Test_RegisterCluster(t *testing.T) {
	t.Run("Returns nil when self cluster registration is disabled", func(t *testing.T) {
		kubeclient := kube.NewFakeKubeClient(testNamespace)
		iss := createMockIssuer(t)
		mgr := NewClusterRegistrationManager(false, testNamespace, testResourceProxyAddr, "", kubeclient, iss)

		err := mgr.RegisterCluster(context.Background(), testAgentName)

		assert.NoError(t, err)

		// Verify no secret was created
		_, err = kubeclient.CoreV1().Secrets(testNamespace).Get(
			context.Background(),
			getClusterSecretName(testAgentName),
			metav1.GetOptions{},
		)
		assert.Error(t, err) // Should not find the secret
	})

	t.Run("Creates cluster secret when it does not exist", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()
		caCertPath := createTestCACertFile(t)

		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", testAgentName).Return("test-bearer-token", nil)

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		err := mgr.RegisterCluster(context.Background(), testAgentName)

		require.NoError(t, err)

		// Verify the cluster secret was created
		secret, err := kubeclient.CoreV1().Secrets(testNamespace).Get(
			context.Background(),
			getClusterSecretName(testAgentName),
			metav1.GetOptions{},
		)
		require.NoError(t, err)
		assert.NotNil(t, secret)
		assert.Equal(t, getClusterSecretName(testAgentName), secret.Name)
	})

	t.Run("Returns error when CA file is missing", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()

		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", testAgentName).Return("test-bearer-token", nil)

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, "/nonexistent/ca.crt", kubeclient, iss)

		err := mgr.RegisterCluster(context.Background(), testAgentName)

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create cluster secret")
	})

	t.Run("Creates cluster secret with correct agent name label", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()
		caCertPath := createTestCACertFile(t)

		agentName := "my-special-agent"
		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", agentName).Return("test-bearer-token", nil)

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		err := mgr.RegisterCluster(context.Background(), agentName)

		require.NoError(t, err)

		// Verify the cluster secret has the correct agent name in its labels
		secret, err := kubeclient.CoreV1().Secrets(testNamespace).Get(
			context.Background(),
			getClusterSecretName(agentName),
			metav1.GetOptions{},
		)
		require.NoError(t, err)
		assert.Equal(t, agentName, secret.Labels[cluster.LabelKeyClusterAgentMapping])
	})
}

func Test_IsSelfClusterRegistrationEnabled(t *testing.T) {
	t.Run("Returns true when enabled", func(t *testing.T) {
		kubeclient := kube.NewFakeKubeClient(testNamespace)
		iss := createMockIssuer(t)
		caCertPath := createTestCACertFile(t)
		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)
		assert.True(t, mgr.IsSelfClusterRegistrationEnabled())
	})

	t.Run("Returns false when disabled", func(t *testing.T) {
		kubeclient := kube.NewFakeKubeClient(testNamespace)
		iss := createMockIssuer(t)
		mgr := NewClusterRegistrationManager(false, testNamespace, testResourceProxyAddr, "", kubeclient, iss)
		assert.False(t, mgr.IsSelfClusterRegistrationEnabled())
	})
}

func Test_RegisterCluster_TokenValidation(t *testing.T) {
	t.Run("Skips registration when cluster secret exists with valid token", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()
		caCertPath := createTestCACertFile(t)

		// Create mock issuer that returns a valid token
		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", testAgentName).Return("valid-token", nil)

		// Create mock claims for token validation
		mockClaims := issuermocks.NewClaims(t)
		mockClaims.On("GetSubject").Return(testAgentName, nil)
		iss.On("ValidateResourceProxyToken", "valid-token").Return(mockClaims, nil)

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		// First registration - creates secret
		err := mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)

		// Second registration - should skip because token is valid
		err = mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)
	})

	t.Run("Refreshes token when existing token is invalid", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()
		caCertPath := createTestCACertFile(t)

		// Create mock issuer
		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", testAgentName).Return("new-token", nil)

		// First call creates secret, subsequent calls validate/refresh
		iss.On("ValidateResourceProxyToken", "new-token").Return(nil, fmt.Errorf("token validation failed"))

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		// First registration - creates secret
		err := mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)

		// Second registration - token validation fails, should refresh
		err = mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)
	})

	t.Run("Refreshes token when subject does not match agent name", func(t *testing.T) {
		kubeclient := kube.NewFakeClientsetWithResources()
		caCertPath := createTestCACertFile(t)

		// Create mock issuer
		iss := issuermocks.NewIssuer(t)
		iss.On("IssueResourceProxyToken", testAgentName).Return("token-for-agent", nil)

		// Mock claims that return wrong subject
		mockClaims := issuermocks.NewClaims(t)
		mockClaims.On("GetSubject").Return("different-agent", nil)
		iss.On("ValidateResourceProxyToken", "token-for-agent").Return(mockClaims, nil)

		mgr := NewClusterRegistrationManager(true, testNamespace, testResourceProxyAddr, caCertPath, kubeclient, iss)

		// First registration - creates secret
		err := mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)

		// Second registration - subject mismatch, should refresh
		err = mgr.RegisterCluster(context.Background(), testAgentName)
		require.NoError(t, err)
	})
}
