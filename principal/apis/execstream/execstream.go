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

package execstream

import (
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/argoproj-labs/argocd-agent/internal/logging"
	"github.com/argoproj-labs/argocd-agent/pkg/api/grpc/execstreamapi"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// Server implements the ExecStreamService gRPC server.
// It maintains active exec sessions and streams data between WebSocket and gRPC.
type Server struct {
	execstreamapi.UnimplementedExecStreamServiceServer
	sessions *SessionManager
}

// SessionManager manages active exec sessions.
type SessionManager struct {
	mutex    sync.RWMutex
	sessions map[string]*ExecSession
}

// ExecSession represents an active exec session.
type ExecSession struct {
	UUID           string
	AgentName      string
	HTTPWriter     http.ResponseWriter
	WSConn         *websocket.Conn
	ToAgent        chan *execstreamapi.ExecStreamData
	FromAgent      chan *execstreamapi.ExecStreamData
	Done           chan struct{}
	CloseOnce      sync.Once
	AgentConnected chan struct{} // Signal when agent connects
	agentOnce      sync.Once
}

// NewServer creates a new ExecStream gRPC server.
func NewServer() *Server {
	return &Server{
		sessions: &SessionManager{
			sessions: make(map[string]*ExecSession),
		},
	}
}

// StreamExec implements the bidirectional streaming RPC for exec sessions.
// This is called by the agent to establish the exec stream.
func (s *Server) StreamExec(stream execstreamapi.ExecStreamService_StreamExecServer) error {
	logCtx := log().WithField("method", "StreamExec")
	logCtx.Info("Agent connected to exec stream")

	// Read the first message to get the session UUID
	firstMsg, err := stream.Recv()
	if err != nil {
		logCtx.WithError(err).Error("Failed to receive first message from agent")
		return err
	}

	sessionUUID := firstMsg.RequestUuid
	if sessionUUID == "" {
		logCtx.Error("Session UUID is empty")
		return fmt.Errorf("session UUID is required")
	}

	logCtx = logCtx.WithField("session_uuid", sessionUUID)

	// Get the session
	session := s.sessions.Get(sessionUUID)
	if session == nil {
		logCtx.Error("Session not found")
		return fmt.Errorf("session %s not found", sessionUUID)
	}

	// Signal that agent has connected
	session.agentOnce.Do(func() {
		close(session.AgentConnected)
	})

	logCtx.Info("Exec session established with agent")

	// Handle incoming data from agent and send to WebSocket
	go func() {
		defer func() {
			session.Close()
			// Close the channels when agent finishes to terminate the exec
			// Use defer/recover to handle case where channels already closed
			func() {
				defer func() { recover() }()
				close(session.ToAgent)
			}()
			func() {
				defer func() { recover() }()
				close(session.FromAgent)
			}()
			logCtx.Info("Agent→WebSocket goroutine exited, channels closed")
		}()

		// Process the first message
		if err := s.sendToWebSocket(session, firstMsg, logCtx); err != nil {
			logCtx.WithError(err).Error("Failed to send first message to WebSocket")
			return
		}

		// Process subsequent messages
		for {
			msg, err := stream.Recv()
			if err != nil {
				if err == io.EOF {
					logCtx.Info("Agent closed the stream")
				} else {
					logCtx.WithError(err).Error("Error receiving from agent")
				}
				return
			}

			logCtx.WithField("data_size", len(msg.Data)).Info("Received data from agent via gRPC")

			if err := s.sendToWebSocket(session, msg, logCtx); err != nil {
				logCtx.WithError(err).Error("Failed to send to WebSocket")
				return
			}
		}
	}()

	// Handle outgoing data from WebSocket to agent
	for {
		select {
		case <-stream.Context().Done():
			logCtx.Info("Stream context done")
			return stream.Context().Err()
		case msg, ok := <-session.ToAgent:
			if !ok {
				logCtx.Info("ToAgent channel closed")
				return nil
			}

			if err := stream.Send(msg); err != nil {
				logCtx.WithError(err).Error("Failed to send to agent")
				return err
			}
		}
	}
}

// sendToWebSocket sends data from the agent to the FromAgent channel.
// The Agent→WebSocket goroutine in processExecRequest will read from this channel.
func (s *Server) sendToWebSocket(session *ExecSession, msg *execstreamapi.ExecStreamData, logCtx *logrus.Entry) error {
	// Send to FromAgent channel instead of writing directly to WebSocket
	// This allows the goroutine in principal/exec.go to continue reading even after WebSocket closes
	select {
	case session.FromAgent <- msg:
		logCtx.WithField("data_size", len(msg.Data)).Debug("Forwarded data to FromAgent channel")
		return nil
	case <-session.Done:
		// Session closed, don't block
		logCtx.Debug("Session closed, cannot forward to FromAgent channel")
		return nil
	}
}

// RegisterSession registers a new exec session.
func (s *Server) RegisterSession(session *ExecSession) {
	s.sessions.Add(session)
}

// UnregisterSession removes an exec session.
func (s *Server) UnregisterSession(sessionUUID string) {
	s.sessions.Remove(sessionUUID)
}

// SessionManager methods

func (sm *SessionManager) Add(session *ExecSession) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	sm.sessions[session.UUID] = session
}

func (sm *SessionManager) Get(uuid string) *ExecSession {
	sm.mutex.RLock()
	defer sm.mutex.RUnlock()
	return sm.sessions[uuid]
}

func (sm *SessionManager) Remove(uuid string) {
	sm.mutex.Lock()
	defer sm.mutex.Unlock()
	if session, exists := sm.sessions[uuid]; exists {
		session.Close()
		delete(sm.sessions, uuid)
	}
}

// ExecSession methods

func (es *ExecSession) Close() {
	es.CloseOnce.Do(func() {
		// Close Done channel to signal goroutines to exit
		select {
		case <-es.Done:
			// Already closed
		default:
			close(es.Done)
		}

		// Note: ToAgent and FromAgent channels are closed by processExecRequest
		// after the grace period, not here

		if es.WSConn != nil {
			_ = es.WSConn.Close()
		}
	})
}

func log() *logrus.Entry {
	return logging.ModuleLogger("execstream")
}
