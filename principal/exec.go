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
	"context"
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

// K8s exec protocol channel numbers
const (
	k8sChannelStdin  = 0
	k8sChannelStdout = 1
	k8sChannelStderr = 2
	k8sChannelError  = 3
	k8sChannelResize = 4
)

// Buffer sizes for exec session channels
const (
	execChannelBufferSize = 100
)

// execUpgrader will convert HTTP exec requests into WebSocket.
var execUpgrader = websocket.Upgrader{
	Subprotocols: append(
		[]string{remotecommandconsts.StreamProtocolV5Name},
		remotecommandconsts.SupportedStreamingProtocols...,
	),
	CheckOrigin: func(r *http.Request) bool {
		// TODO: Need to implement logic to allow only ArgoCD UI to connect
		return true
	},
}

const execSessionTimeout = 30 * time.Minute

// processExecRequest handles an exec subresource request by upgrading the HTTP connection to a WebSocket,
// verifying agent connection, and implementing exec streaming to a target pod/container via the connected agent.
func (s *Server) processExecRequest(w http.ResponseWriter, r *http.Request, params resourceproxy.Params, agentName string) {
	logCtx := log().WithField("function", "processExecRequest")

	// Extract parameters from the request
	namespace := params.Get("namespace")
	podName := params.Get("name")
	containerName := r.URL.Query().Get("container")
	command := r.URL.Query()["command"]

	if podName == "" {
		logCtx.Error("Pod name is required")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("pod name is required"))
		return
	}

	if namespace == "" {
		logCtx.Error("Namespace is required")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("namespace is required"))
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
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("agent not connected"))
		return
	}

	// Upgrade HTTP request to WebSocket
	wsConn, err := execUpgrader.Upgrade(w, r, nil)
	if err != nil {
		logCtx.WithError(err).Error("Failed to upgrade to WebSocket")
		return
	}
	defer wsConn.Close()

	logCtx.Info("WebSocket connection upgraded")

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
		UUID:      sessionUUID,
		AgentName: agentName,
		WSConn:    wsConn,
		ToAgent:   make(chan *execstreamapi.ExecStreamData, execChannelBufferSize),
		FromAgent: make(chan *execstreamapi.ExecStreamData, execChannelBufferSize),
		Done:      make(chan struct{}),
	}

	// Register the session
	s.execStreamServer.RegisterSession(session)
	defer s.execStreamServer.UnregisterSession(sessionUUID)

	logCtx.Info("Exec session registered")

	// Send exec request event to agent
	execEvent, err := s.events.NewExecRequestEvent(execReq)
	if err != nil {
		logCtx.WithError(err).Error("Failed to create exec event")
		return
	}

	q := s.queues.SendQ(agentName)
	if q == nil {
		logCtx.Error("Send queue not found")
		return
	}

	q.Add(execEvent)
	logCtx.Info("Exec request event sent to agent")

	go s.agentToWebSocketChannel(session, logCtx)
	go s.webSocketToAgentChannel(session, logCtx)

	ctx, cancel := context.WithTimeout(s.ctx, execSessionTimeout)
	defer cancel()

	// Wait for session to complete or time out
	select {
	case <-session.Done:
		logCtx.Info("Exec session completed, cleaning up")
	case <-ctx.Done():
		// Overall session timeout (for long-running terminals)
		logCtx.Warn("Session timeout after 30 minutes")
	}

	// Ensure channels are closed to terminate the agent exec stream
	closeExecStreamChannels(session)

	logCtx.Info("Exec session unregistered")
}

func (s *Server) agentToWebSocketChannel(session *execstream.ExecSession, logCtx *logrus.Entry) {
	wsConn := session.WSConn
	defer func() {
		logCtx.Info("Agent to WebSocket channel goroutine exited")
		select {
		case <-session.Done:
		default:
			close(session.Done)
		}
	}()

	for {
		data, ok := <-session.FromAgent
		if !ok {
			logCtx.Info("FromAgent channel closed")
			return
		}

		if data.Error != "" {
			logCtx.WithField("error", data.Error).Error("Agent reported error")
			notifyWebSocketError(wsConn, session.Done, data.Error, logCtx)
			return
		}

		if data.Eof {
			logCtx.Info("Agent sent EOF")
			notifyWebSocketEOF(wsConn, session.Done, logCtx)
			return
		}

		if len(data.Data) == 0 {
			continue
		}

		channel := streamChannel(data.StreamType)
		wsData := make([]byte, len(data.Data)+1)
		wsData[0] = channel
		copy(wsData[1:], data.Data)

		select {
		case <-session.Done:
			logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Debug("Dropping data after session closed")
		default:
			if err := wsConn.WriteMessage(websocket.BinaryMessage, wsData); err != nil {
				logCtx.WithError(err).Error("Failed to write to WebSocket")
				return
			}
			logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Debug("Sent data to WebSocket")
		}
	}
}

func (s *Server) webSocketToAgentChannel(session *execstream.ExecSession, logCtx *logrus.Entry) {
	wsConn := session.WSConn
	defer func() {
		logCtx.Info("WebSocket to Agent channel goroutine exited")
		select {
		case <-session.Done:
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

		if messageType != websocket.BinaryMessage && messageType != websocket.TextMessage {
			continue
		}

		if s.handleResizeMessage(session, data, logCtx) {
			continue
		}

		stdinData := normalizeStdinPayload(data)
		select {
		case session.ToAgent <- &execstreamapi.ExecStreamData{
			RequestUuid: session.UUID,
			Data:        stdinData,
			StreamType:  "stdin",
		}:
		case <-session.Done:
			return
		}
	}
}

func (s *Server) handleResizeMessage(session *execstream.ExecSession, data []byte, logCtx *logrus.Entry) bool {
	if len(data) == 0 {
		return false
	}

	// Check for K8s exec channel format (first byte is channel number)
	// Channel 4 is the resize channel in K8s exec protocol
	if data[0] == k8sChannelResize && len(data) > 1 {
		return s.handleK8sResizeMessage(session, data[1:], logCtx)
	}

	// Check for JSON resize messages
	payload := data
	// If first byte looks like a channel prefix (0-4), strip it
	if len(data) > 1 && data[0] <= k8sChannelResize && data[1] == '{' {
		payload = data[1:]
	}

	if len(payload) == 0 || payload[0] != '{' {
		return false
	}

	return s.handleJSONResizeMessage(session, payload, logCtx)
}

// sendResizeToAgent sends a resize event to the agent
func (s *Server) sendResizeToAgent(session *execstream.ExecSession, cols, rows uint32, logCtx *logrus.Entry) {
	logCtx.WithFields(logrus.Fields{
		"cols": cols,
		"rows": rows,
	}).Debug("Sending terminal resize to agent")

	select {
	case session.ToAgent <- &execstreamapi.ExecStreamData{
		RequestUuid: session.UUID,
		Resize:      true,
		Cols:        cols,
		Rows:        rows,
	}:
	case <-session.Done:
	}
}

func (s *Server) handleK8sResizeMessage(session *execstream.ExecSession, data []byte, logCtx *logrus.Entry) bool {
	cols, rows, ok := parseK8sResizeFormat(data)
	if !ok {
		return false
	}
	s.sendResizeToAgent(session, cols, rows, logCtx)
	return true
}

func (s *Server) handleJSONResizeMessage(session *execstream.ExecSession, data []byte, logCtx *logrus.Entry) bool {
	// Try K8s format first: {"Width":cols,"Height":rows}
	if cols, rows, ok := parseK8sResizeFormat(data); ok {
		s.sendResizeToAgent(session, cols, rows, logCtx)
		return true
	}

	// Try operation format: {"operation":"resize","cols":...,"rows":...}
	var resizeMsg struct {
		Operation string `json:"operation"`
		Cols      uint32 `json:"cols"`
		Rows      uint32 `json:"rows"`
	}

	if err := json.Unmarshal(data, &resizeMsg); err != nil || resizeMsg.Operation != "resize" {
		return false
	}

	s.sendResizeToAgent(session, resizeMsg.Cols, resizeMsg.Rows, logCtx)
	return true
}

// parseK8sResizeFormat parses the Kubernetes resize message format: {"Width":cols,"Height":rows}
func parseK8sResizeFormat(data []byte) (cols, rows uint32, ok bool) {
	var k8sResize struct {
		Width  uint32 `json:"Width"`
		Height uint32 `json:"Height"`
	}
	if err := json.Unmarshal(data, &k8sResize); err != nil {
		return 0, 0, false
	}
	if k8sResize.Width == 0 && k8sResize.Height == 0 {
		return 0, 0, false
	}
	return k8sResize.Width, k8sResize.Height, true
}

func streamChannel(streamType string) byte {
	switch streamType {
	case "stderr":
		return k8sChannelStderr
	case "error":
		return k8sChannelError
	default:
		return k8sChannelStdout
	}
}

func normalizeStdinPayload(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	// Strip K8s stdin channel prefix if present
	if data[0] == k8sChannelStdin {
		return data[1:]
	}
	return data
}

func notifyWebSocketError(wsConn *websocket.Conn, done <-chan struct{}, msg string, logCtx *logrus.Entry) {
	select {
	case <-done:
		logCtx.Debug("WebSocket closed, cannot send error")
	default:
		_ = wsConn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}

func notifyWebSocketEOF(wsConn *websocket.Conn, done <-chan struct{}, logCtx *logrus.Entry) {
	select {
	case <-done:
		logCtx.Debug("WebSocket closed, cannot send EOF")
	default:
		_ = wsConn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		)
	}
}

func closeExecStreamChannels(session *execstream.ExecSession) {
	safeCloseExecStreamDataChan(session.ToAgent)
	safeCloseExecStreamDataChan(session.FromAgent)
}

func safeCloseExecStreamDataChan(ch chan *execstreamapi.ExecStreamData) {
	defer func() { recover() }()
	close(ch)
}
