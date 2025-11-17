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
	"io"
	"time"

	"github.com/argoproj-labs/argocd-agent/internal/event"
	"github.com/argoproj-labs/argocd-agent/internal/logging/logfields"
	"github.com/argoproj-labs/argocd-agent/pkg/api/grpc/execstreamapi"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// processIncomingExecRequest handles incoming exec requests from the principal.
// It establishes a Kubernetes exec session and bridges it with the gRPC stream.
func (a *Agent) processIncomingExecRequest(ev *event.Event) error {
	execReq, err := ev.ExecRequest()
	if err != nil {
		return fmt.Errorf("failed to parse exec request: %w", err)
	}

	logCtx := log().WithFields(logrus.Fields{
		"method":            "processIncomingExecRequest",
		"session_uuid":      execReq.UUID,
		logfields.Namespace: execReq.Namespace,
		"pod":               execReq.PodName,
		"container":         execReq.ContainerName,
	})

	logCtx.Info("Processing exec request")

	// Connect to principal's exec stream service
	conn := a.remote.Conn()
	execStreamClient := execstreamapi.NewExecStreamServiceClient(conn)

	stream, err := execStreamClient.StreamExec(a.context)
	if err != nil {
		logCtx.WithError(err).Error("Failed to open exec stream to principal")
		return err
	}

	logCtx.Info("Exec stream to principal established")

	// Execute the K8s exec API call
	if err := a.execInPod(a.context, stream, execReq, logCtx); err != nil {
		logCtx.WithError(err).Error("Exec failed")
		// Send error to principal
		_ = stream.Send(&execstreamapi.ExecStreamData{
			RequestUuid: execReq.UUID,
			Error:       err.Error(),
		})
		return err
	}

	logCtx.Info("Exec completed successfully")
	return nil
}

// execInPod executes a command in a pod and streams I/O via gRPC.
func (a *Agent) execInPod(ctx context.Context, stream execstreamapi.ExecStreamService_StreamExecClient, execReq *event.ContainerExecRequest, logCtx *logrus.Entry) error {
	// Build Kubernetes exec request
	req := a.kubeClient.Clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Name(execReq.PodName).
		Namespace(execReq.Namespace).
		SubResource("exec")

	// Set exec options
	execOptions := &corev1.PodExecOptions{
		Container: execReq.ContainerName,
		Command:   execReq.Command,
		Stdin:     execReq.Stdin,
		Stdout:    execReq.Stdout,
		Stderr:    execReq.Stderr,
		TTY:       execReq.TTY,
	}

	// Default command if not specified
	if len(execOptions.Command) == 0 {
		execOptions.Command = []string{"/bin/sh"}
	}

	req.VersionedParams(execOptions, scheme.ParameterCodec)

	logCtx.Infof("Executing command: %v", execOptions.Command)

	// Create SPDY executor
	exec, err := remotecommand.NewSPDYExecutor(a.kubeClient.RestConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("failed to create SPDY executor: %w", err)
	}

	// Try WebSocket executor as fallback
	websocketExec, wsErr := remotecommand.NewWebSocketExecutor(a.kubeClient.RestConfig, "GET", req.URL().String())
	if wsErr == nil {
		fallbackExec, fallbackErr := remotecommand.NewFallbackExecutor(websocketExec, exec, func(err error) bool {
			return true // Always try fallback
		})
		if fallbackErr == nil {
			exec = fallbackExec
		}
	}

	// Create cancellable context for the exec
	// This allows us to terminate the exec when EOF is received from principal
	execCtx, cancelExec := context.WithCancel(ctx)
	defer cancelExec()

	// Create terminal size queue with default dimensions
	// This is CRITICAL - shells won't produce prompts without terminal size
	sizeQueue := &terminalSizeQueue{
		sizes: make(chan *remotecommand.TerminalSize, 10), // Buffer for resize events
	}
	// Send initial terminal size (80x24 is standard default)
	sizeQueue.sizes <- &remotecommand.TerminalSize{
		Width:  80,
		Height: 24,
	}

	// Create stream handler
	streamHandler := &execStreamHandler{
		stream:      stream,
		sessionUUID: execReq.UUID,
		stdin:       make(chan []byte, 100),
		done:        make(chan struct{}),
		cancelExec:  cancelExec,
		sizeQueue:   sizeQueue,
		logCtx:      logCtx,
	}

	// Start goroutine to receive stdin from principal and forward to K8s
	go streamHandler.receiveFromPrincipal()

	// Send an initial newline to trigger shell prompt
	// This is crucial for ArgoCD's shell detection during testing
	// Without this, bash/sh start but produce no output
	go func() {
		// Wait a tiny bit for StreamWithContext to start
		time.Sleep(100 * time.Millisecond)
		select {
		case streamHandler.stdin <- []byte("\n"):
			logCtx.Info("Sent initial newline to trigger shell prompt")
		case <-streamHandler.done:
			logCtx.Info("Stream closed before newline could be sent")
		}
	}()

	// Execute with the stream handler
	err = exec.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdin:             streamHandler,
		Stdout:            streamHandler,
		Stderr:            streamHandler,
		Tty:               execReq.TTY,
		TerminalSizeQueue: sizeQueue,
	})

	// Close the stream
	close(streamHandler.done)

	if err != nil {
		logCtx.WithError(err).Error("StreamWithContext failed")
		return err
	}

	// Send EOF to principal
	_ = stream.Send(&execstreamapi.ExecStreamData{
		RequestUuid: execReq.UUID,
		Eof:         true,
	})

	logCtx.Info("Exec stream completed")
	return nil
}

// execStreamHandler implements io.Reader and io.Writer to bridge K8s exec with gRPC.
type execStreamHandler struct {
	stream      execstreamapi.ExecStreamService_StreamExecClient
	sessionUUID string
	stdin       chan []byte
	done        chan struct{}
	cancelExec  context.CancelFunc
	sizeQueue   *terminalSizeQueue
	logCtx      *logrus.Entry
}

// terminalSizeQueue implements remotecommand.TerminalSizeQueue
type terminalSizeQueue struct {
	sizes chan *remotecommand.TerminalSize
}

func (t *terminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-t.sizes
	if !ok {
		return nil
	}
	return size
}

// Read implements io.Reader - reads stdin data from the gRPC stream.
func (h *execStreamHandler) Read(p []byte) (int, error) {
	select {
	case <-h.done:
		return 0, io.EOF
	case data, ok := <-h.stdin:
		if !ok {
			return 0, io.EOF
		}
		n := copy(p, data)
		h.logCtx.WithField("data_size", n).Debug("Shell read stdin data")
		return n, nil
	}
}

// Write implements io.Writer - writes stdout/stderr data to the gRPC stream.
func (h *execStreamHandler) Write(p []byte) (int, error) {
	data := make([]byte, len(p))
	copy(data, p)

	h.logCtx.WithField("data_size", len(data)).Info("Shell produced output, sending to principal")

	err := h.stream.Send(&execstreamapi.ExecStreamData{
		RequestUuid: h.sessionUUID,
		Data:        data,
		StreamType:  "stdout",
	})

	if err != nil {
		h.logCtx.WithError(err).Error("Failed to send data to principal")
		return 0, err
	}

	h.logCtx.WithField("data_size", len(data)).Debug("Data sent to principal successfully")
	return len(p), nil
}

// receiveFromPrincipal receives data from the principal and forwards to stdin.
func (h *execStreamHandler) receiveFromPrincipal() {
	defer close(h.stdin)

	for {
		select {
		case <-h.done:
			return
		default:
		}

		msg, err := h.stream.Recv()
		if err != nil {
			if err == io.EOF {
				h.logCtx.Info("Principal closed exec stream")
			} else {
				h.logCtx.WithError(err).Error("Error receiving from principal")
			}
			// Stream closed, stop receiving
			// Don't cancel exec - let it complete naturally
			return
		}

		if msg.Error != "" {
			h.logCtx.WithField("error", msg.Error).Error("Principal reported error, cancelling exec")
			h.cancelExec()
			return
		}

		if msg.Eof {
			h.logCtx.Info("Principal sent EOF, closing stdin")
			// Close stdin channel to signal end of input
			// Don't cancel exec - let it complete naturally
			return
		}

		// Handle terminal resize
		if msg.Resize {
			h.logCtx.WithFields(logrus.Fields{
				"cols": msg.Cols,
				"rows": msg.Rows,
			}).Info("Received terminal resize from principal")
			// Send resize to terminal size queue
			select {
			case h.sizeQueue.sizes <- &remotecommand.TerminalSize{
				Width:  uint16(msg.Cols),
				Height: uint16(msg.Rows),
			}:
			case <-h.done:
				return
			default:
				// Queue full, skip this resize
				h.logCtx.Warn("Terminal size queue full, skipping resize")
			}
			continue
		}

		if len(msg.Data) > 0 && msg.StreamType == "stdin" {
			select {
			case h.stdin <- msg.Data:
			case <-h.done:
				return
			}
		}
	}
}
