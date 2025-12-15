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

const (
	k8sChannelStdin  = 0
	k8sChannelStdout = 1
	k8sChannelStderr = 2
	k8sChannelError  = 3
	k8sChannelResize = 4
)

const (
	execChannelBufferSize = 100
)

// execUpgrader will convert HTTP web terminal requests into WebSocket.
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

// processExecRequest handles a web terminal request by upgrading the HTTP connection to a WebSocket,
// verifying agent connection, and implementing web terminal streaming to a target pod/container via the connected agent.
func (s *Server) processExecRequest(w http.ResponseWriter, r *http.Request, params resourceproxy.Params, agentName string) {
	logCtx := log().WithField("function", "processExecRequest")

	namespace := params.Get("namespace")
	podName := params.Get("name")
	containerName := r.URL.Query().Get("container")
	command := r.URL.Query()["command"]

	logCtx = logCtx.WithFields(logrus.Fields{
		logfields.Namespace: namespace,
		"pod":               podName,
		"container":         containerName,
		"command":           command,
	})

	logCtx.Info("Processing web terminal request")

	// Upgrade HTTP request to WebSocket
	// WebSocket connection is used to stream data back and forth between browser and principal
	// This is a bidirectional connection which stays open for the duration of the web terminal session
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

	// Create web terminal request
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

	// Create web terminal session
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

	logCtx.Info("Web terminal session registered")

	// Send web terminal request event to agent
	execEvent, err := s.events.NewExecRequestEvent(execReq)
	if err != nil {
		logCtx.WithError(err).Error("Failed to create web terminal request event")
		return
	}

	q := s.queues.SendQ(agentName)
	if q == nil {
		logCtx.Error("Send queue not found")
		return
	}

	q.Add(execEvent)
	logCtx.Info("Web terminal request event sent to agent")

	go s.agentToWebSocketChannel(session, logCtx)
	go s.webSocketToAgentChannel(session, logCtx)

	ctx, cancel := context.WithTimeout(s.ctx, execSessionTimeout)
	defer cancel()

	// Wait for session to complete or time out
	select {
	case <-session.Done:
		logCtx.Info("Web terminal session completed, cleaning up")
	case <-ctx.Done():
		// Overall session timeout (for long-running terminals)
		logCtx.Warn("Web terminal session timeout after 30 minutes")
	}

	// Ensure channels are closed to terminate the agent web terminal stream
	closeExecStreamChannels(session)

	logCtx.Info("Web terminal session unregistered")
}

// webSocketToAgentChannel is a goroutine that reads data from the WebSocket for the browser and writes it to the agent.
// This data is input to commands executed in the application pod running in managed cluster.
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

		// Get stdin data from the message
		var stdinData []byte
		if len(data) > 0 && data[0] == k8sChannelStdin {
			stdinData = data[1:]
		} else {
			stdinData = data
		}

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

// agentToWebSocketChannel is a goroutine that reads data from the agent and writes it to the WebSocket for the browser.
// This data is output of commands executed in the application pod running in managed cluster.
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
			logCtx.WithField("error", data.Error).Error("Agent reported error, closing web terminal session")
			notifyWebSocketError(wsConn, session.Done, data.Error, logCtx)
			return
		}

		if data.Eof {
			logCtx.Info("Agent sent EOF, closing web terminal session")
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
			logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Debug("Dropping data after web terminal session closed")
		default:
			if err := wsConn.WriteMessage(websocket.BinaryMessage, wsData); err != nil {
				logCtx.WithError(err).Error("Failed to write to WebSocket, closing web terminal session")
				return
			}
			logCtx.WithField("data_size", len(data.Data)).WithField("channel", channel).Debug("Sent data to WebSocket")
		}
	}
}

// handleResizeMessage handles terminal resize messages from the ArgoCD UI browser.
func (s *Server) handleResizeMessage(session *execstream.ExecSession, data []byte, logCtx *logrus.Entry) bool {
	if len(data) == 0 {
		return false
	}

	// Get JSON payload
	payload := data
	if len(data) > 1 && data[0] <= k8sChannelResize && data[1] == '{' {
		payload = data[1:]
	}

	// Quick check: if not JSON, it's not a resize message
	if len(payload) == 0 || payload[0] != '{' {
		return false
	}

	// Parse ArgoCD UI resize format: {"operation":"resize","cols":...,"rows":...}
	var resizeMsg struct {
		Operation string `json:"operation"`
		Cols      uint32 `json:"cols"`
		Rows      uint32 `json:"rows"`
	}

	if err := json.Unmarshal(payload, &resizeMsg); err != nil || resizeMsg.Operation != "resize" {
		return false
	}

	// Send resize event to agent. The shell running in application pod needs to be notified of the new size
	// So that shell can render the output content accordingly.
	s.sendResizeToAgent(session, resizeMsg.Cols, resizeMsg.Rows, logCtx)
	return true
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

// streamChannel converts the stream type to the corresponding channel number
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

// notifyWebSocketError sends an error message to the WebSocket
func notifyWebSocketError(wsConn *websocket.Conn, done <-chan struct{}, msg string, logCtx *logrus.Entry) {
	select {
	case <-done:
		logCtx.Debug("WebSocket closed, cannot send error")
	default:
		_ = wsConn.WriteMessage(websocket.TextMessage, []byte(msg))
	}
}

// notifyWebSocketEOF sends an EOF message to the WebSocket
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

// closeExecStreamChannels closes the channels for the web terminal session
func closeExecStreamChannels(session *execstream.ExecSession) {
	safeCloseExecStreamDataChan(session.ToAgent)
	safeCloseExecStreamDataChan(session.FromAgent)
}

// safeCloseExecStreamDataChan safely closes the channel
func safeCloseExecStreamDataChan(ch chan *execstreamapi.ExecStreamData) {
	defer func() { recover() }()
	close(ch)
}
