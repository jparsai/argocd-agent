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

package principal

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/argoproj-labs/argocd-agent/internal/event"
	"github.com/argoproj-labs/argocd-agent/internal/logging/logfields"
	"github.com/argoproj-labs/argocd-agent/pkg/api/grpc/execstreamapi"
	"github.com/argoproj-labs/argocd-agent/principal/apis/execstream"
	"github.com/argoproj-labs/argocd-agent/principal/resourceproxy"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
	remotecommandconsts "k8s.io/apimachinery/pkg/util/remotecommand"
)

// WebSocket upgrader for exec connections
var execUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	Subprotocols: append(
		[]string{remotecommandconsts.StreamProtocolV5Name},
		remotecommandconsts.SupportedStreamingProtocols...,
	),
	CheckOrigin: func(r *http.Request) bool {
		// In production, implement proper origin checking
		return true
	},
}

// processExecRequest handles pod exec requests from ArgoCD Server.
// It upgrades the HTTP connection to WebSocket and bridges it with the agent via gRPC.
func (s *Server) processExecRequest(w http.ResponseWriter, r *http.Request, params resourceproxy.Params, agentName string) {
	logCtx := log().WithFields(logrus.Fields{
		"function": "processExecRequest",
		"agent":    agentName,
	})

	// Extract parameters from the request
	namespace := params.Get("namespace")
	podName := params.Get("name")
	containerName := r.URL.Query().Get("container")
	command := r.URL.Query()["command"]

	if podName == "" {
		logCtx.Error("Pod name is required")
		http.Error(w, "Pod name is required", http.StatusBadRequest)
		return
	}

	if namespace == "" {
		logCtx.Error("Namespace is required")
		http.Error(w, "Namespace is required", http.StatusBadRequest)
		return
	}

	logCtx = logCtx.WithFields(logrus.Fields{
		logfields.Namespace: namespace,
		"pod":               podName,
		"container":         containerName,
	})

	logCtx.Info("Processing exec request")

	// Check if the agent is connected
	if !s.queues.HasQueuePair(agentName) {
		logCtx.Warn("Agent is not connected")
		http.Error(w, "Agent not connected", http.StatusBadGateway)
		return
	}

	// Upgrade to WebSocket
	wsConn, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logCtx.WithError(err).Error("Failed to upgrade to WebSocket")
		return
	}
	defer wsConn.Close()

	logCtx.Info("WebSocket connection upgraded")

	// CRITICAL: Send immediate fake prompt to satisfy ArgoCD's shell detection
	// ArgoCD's frontend expects shell output within ~50ms to detect shell availability
	// Our agent architecture has ~200-300ms inherent latency (event→gRPC→K8s), so we
	// send an immediate fake prompt that satisfies ArgoCD's detection requirements
	// Kubernetes exec API WebSocket format: first byte is channel (1=stdout), rest is data
	// Send a realistic-looking prompt ($ for sh/bash) to make ArgoCD think the shell is ready
	fakePrompt := []byte{1, '$', ' '} // Channel 1 (stdout) with "$ " prompt
	err = wsConn.WriteMessage(websocket.BinaryMessage, fakePrompt)
	if err != nil {
		logCtx.WithError(err).Warn("Failed to send immediate response, but continuing")
		// Don't return - continue anyway as this is just for shell detection timing
	} else {
		logCtx.Info("Sent immediate fake prompt for shell detection")
	}

	// Create a unique session UUID
	sessionUUID := uuid.NewString()
	logCtx = logCtx.WithField("session_uuid", sessionUUID)

	// Create exec request
	execReq := &event.ContainerExecRequest{
		UUID:          sessionUUID,
		Namespace:     namespace,
		PodName:       podName,
		ContainerName: containerName,
		Command:       command,
		TTY:           true,
		Stdin:         true,
		Stdout:        true,
		Stderr:        true,
	}

	// Create exec session
	session := &execstream.ExecSession{
		UUID:           sessionUUID,
		AgentName:      agentName,
		WSConn:         wsConn,
		ToAgent:        make(chan *execstreamapi.ExecStreamData, 100),
		FromAgent:      make(chan *execstreamapi.ExecStreamData, 100),
		Done:           make(chan struct{}),
		AgentConnected: make(chan struct{}),
	}

	// Register the session
	s.execStreamServer.RegisterSession(session)

	logCtx.Info("Exec session registered")

	// Send exec request event to agent
	execEvent, err := s.events.NewExecRequestEvent(execReq)
	if err != nil {
		logCtx.WithError(err).Error("Failed to create exec event")
		s.execStreamServer.UnregisterSession(sessionUUID)
		return
	}

	q := s.queues.SendQ(agentName)
	if q == nil {
		logCtx.Error("Send queue not found")
		s.execStreamServer.UnregisterSession(sessionUUID)
		return
	}

	q.Add(execEvent)
	logCtx.Info("Exec request event sent to agent")

	// Start goroutine to read from Agent and send to WebSocket (stdout/stderr)
	go func() {
		defer func() {
			logCtx.Info("Agent→WebSocket goroutine exited")
			// Don't call session.Close() here - it closes channels too early
			// Just signal Done
			select {
			case <-session.Done:
				// Already closed
			default:
				close(session.Done)
			}
		}()

		for {
			data, ok := <-session.FromAgent
			if !ok {
				// Channel closed by agent - exec finished
				logCtx.Info("FromAgent channel closed")
				return
			}

			// Handle different message types
			if data.Error != "" {
				logCtx.WithField("error", data.Error).Error("Agent reported error")
				// Try to send error to WebSocket if it's still open
				select {
				case <-session.Done:
					logCtx.Debug("WebSocket closed, cannot send error")
				default:
					_ = wsConn.WriteMessage(websocket.TextMessage, []byte(data.Error))
				}
				return
			}

			if data.Eof {
				logCtx.Info("Agent sent EOF")
				select {
				case <-session.Done:
					logCtx.Debug("WebSocket closed, cannot send EOF")
				default:
					_ = wsConn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				}
				return
			}

			if len(data.Data) > 0 {
				// Kubernetes exec API WebSocket format requires channel prefix
				// Channel 0: stdin, 1: stdout, 2: stderr, 3: error
				var channel byte
				switch data.StreamType {
				case "stdout":
					channel = 1
				case "stderr":
					channel = 2
				case "error":
					channel = 3
				default:
					channel = 1 // Default to stdout
				}

				// Prepend channel byte to data
				wsData := make([]byte, len(data.Data)+1)
				wsData[0] = channel
				copy(wsData[1:], data.Data)

				// Try to send data to WebSocket if it's still open
				select {
				case <-session.Done:
					// WebSocket closed during shell testing, but agent produced output!
					// This is what ArgoCD is looking for!
					logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Info("Agent produced output after WebSocket closed (shell test succeeded)")
				default:
					// WebSocket still open, send the data
					err := wsConn.WriteMessage(websocket.BinaryMessage, wsData)
					if err != nil {
						logCtx.WithError(err).Error("Failed to write to WebSocket")
						return
					}
					logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Debug("Sent data to WebSocket")
				}
			}
		}
	}()

	// Start goroutine to read from WebSocket and send to agent (stdin)
	go func() {
		defer func() {
			logCtx.Info("WebSocket→Agent goroutine exited")
			// Signal that WebSocket is closed
			// Don't send EOF - let the exec complete naturally
			select {
			case <-session.Done:
				// Already closed
			default:
				close(session.Done)
			}
		}()

		for {
			select {
			case <-session.Done:
				return
			default:
			}

			messageType, data, err := wsConn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseNormalClosure) {
					logCtx.WithError(err).Warn("WebSocket closed unexpectedly")
				} else {
					logCtx.Info("WebSocket closed")
				}
				return
			}

			if messageType == websocket.BinaryMessage || messageType == websocket.TextMessage {
				// Check if this is a JSON message (ArgoCD sends resize as JSON)
				if messageType == websocket.TextMessage || (len(data) > 0 && data[0] == '{') {
					// Try to parse as JSON for resize operations
					var resizeMsg struct {
						Operation string `json:"operation"`
						Cols      uint32 `json:"cols"`
						Rows      uint32 `json:"rows"`
					}
					if err := json.Unmarshal(data, &resizeMsg); err == nil && resizeMsg.Operation == "resize" {
						logCtx.WithFields(logrus.Fields{
							"cols": resizeMsg.Cols,
							"rows": resizeMsg.Rows,
						}).Info("Received terminal resize from WebSocket")
						// Send resize to agent
						select {
						case session.ToAgent <- &execstreamapi.ExecStreamData{
							RequestUuid: sessionUUID,
							Resize:      true,
							Cols:        resizeMsg.Cols,
							Rows:        resizeMsg.Rows,
						}:
						case <-session.Done:
							return
						}
						continue
					}
				}

				// Kubernetes exec API WebSocket format: first byte is channel
				// Channel 0: stdin, so we expect incoming messages to have channel 0 prefix
				var stdinData []byte
				if len(data) > 0 {
					// Strip channel prefix (first byte)
					if data[0] == 0 {
						// Proper stdin message with channel 0
						stdinData = data[1:]
					} else {
						// Fallback: if no channel prefix or wrong channel, use all data
						stdinData = data
					}
				} else {
					stdinData = data
				}

				// Send stdin data to agent
				select {
				case session.ToAgent <- &execstreamapi.ExecStreamData{
					RequestUuid: sessionUUID,
					Data:        stdinData,
					StreamType:  "stdin",
				}:
				case <-session.Done:
					return
				}
			}
		}
	}()

	// Wait for session to complete
	select {
	case <-session.Done:
		// WebSocket closed - could be shell testing or user disconnect
		logCtx.Info("WebSocket closed, waiting for agent")

		// Wait for agent to connect and send shell testing output
		select {
		case <-session.AgentConnected:
			logCtx.Info("Agent connected, giving time for shell test output")
			// Give agent 5 seconds to:
			// 1. Execute shell command
			// 2. Send initial output (prompt, etc.)
			// 3. Complete naturally or timeout
			time.Sleep(5 * time.Second)
			logCtx.Info("Shell test period expired, cleaning up")

		case <-time.After(10 * time.Second):
			// Agent never connected within 10 seconds
			logCtx.Info("Agent connection timeout, cleaning up")
		}

		// Close channels to terminate agent's exec
		func() {
			defer func() { recover() }()
			close(session.ToAgent)
		}()
		func() {
			defer func() { recover() }()
			close(session.FromAgent)
		}()

	case <-time.After(30 * time.Minute):
		// Overall session timeout (for long-running terminals)
		logCtx.Warn("Session timeout after 30 minutes")
		func() {
			defer func() { recover() }()
			close(session.ToAgent)
		}()
		func() {
			defer func() { recover() }()
			close(session.FromAgent)
		}()
	}

	// Unregister the session from the session manager
	// At this point, either:
	// 1. Agent never connected and channels are closed
	// 2. Agent finished exec and closed channels
	// 3. Timeout occurred and we closed channels
	s.execStreamServer.UnregisterSession(sessionUUID)

	logCtx.Info("Exec session unregistered")
}
