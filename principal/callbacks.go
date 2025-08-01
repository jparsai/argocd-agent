// Copyright 2024 The argocd-agent Authors
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
	"strings"

	"github.com/argoproj-labs/argocd-agent/internal/event"
	"github.com/argoproj-labs/argocd-agent/internal/resources"
	"github.com/argoproj-labs/argocd-agent/pkg/types"
	"github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sirupsen/logrus"
	corev1 "k8s.io/api/core/v1"
)

// newAppCallback is executed when a new application event was emitted from
// the informer and needs to be sent out to an agent. If the receiving agent
// is in autonomous mode, this event will be discarded.
func (s *Server) newAppCallback(outbound *v1alpha1.Application) {
	logCtx := log().WithFields(logrus.Fields{
		"component":        "EventCallback",
		"queue":            outbound.Namespace,
		"event":            "application_new",
		"application_name": outbound.Name,
	})

	s.resources.Add(outbound.Namespace, resources.NewResourceKeyFromApp(outbound))

	if !s.queues.HasQueuePair(outbound.Namespace) {
		if err := s.queues.Create(outbound.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing namespace")
	}
	q := s.queues.SendQ(outbound.Namespace)
	if q == nil {
		logCtx.Errorf("Help! queue pair for namespace %s disappeared!", outbound.Namespace)
		return
	}
	ev := s.events.ApplicationEvent(event.Create, outbound)
	q.Add(ev)
	logCtx.Tracef("Added app %s to send queue, total length now %d", outbound.QualifiedName(), q.Len())

	if s.metrics != nil {
		s.metrics.ApplicationCreated.Inc()
	}
}

func (s *Server) updateAppCallback(old *v1alpha1.Application, new *v1alpha1.Application) {
	s.watchLock.Lock()
	defer s.watchLock.Unlock()
	logCtx := log().WithFields(logrus.Fields{
		"component":        "EventCallback",
		"queue":            old.Namespace,
		"event":            "application_update",
		"application_name": old.Name,
	})
	if s.appManager.IsChangeIgnored(new.QualifiedName(), new.ResourceVersion) {
		logCtx.WithField("resource_version", new.ResourceVersion).Debugf("Resource version has already been seen")
		return
	}
	if !s.queues.HasQueuePair(old.Namespace) {
		if err := s.queues.Create(old.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing agent namespace")
	}
	q := s.queues.SendQ(old.Namespace)
	if q == nil {
		logCtx.Error("Help! Queue pair has disappeared!")
		return
	}
	ev := s.events.ApplicationEvent(event.SpecUpdate, new)
	q.Add(ev)
	logCtx.Tracef("Added app to send queue, total length now %d", q.Len())

	if s.metrics != nil {
		s.metrics.ApplicationUpdated.Inc()
	}
}

func (s *Server) deleteAppCallback(outbound *v1alpha1.Application) {
	logCtx := log().WithFields(logrus.Fields{
		"component":        "EventCallback",
		"queue":            outbound.Namespace,
		"event":            "application_delete",
		"application_name": outbound.Name,
	})

	s.resources.Remove(outbound.Namespace, resources.NewResourceKeyFromApp(outbound))

	if !s.queues.HasQueuePair(outbound.Namespace) {
		if err := s.queues.Create(outbound.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing agent namespace")
	}
	q := s.queues.SendQ(outbound.Namespace)
	if q == nil {
		logCtx.Error("Help! Queue pair has disappeared!")
		return
	}
	ev := s.events.ApplicationEvent(event.Delete, outbound)
	logCtx.WithField("event", "DeleteApp").WithField("sendq_len", q.Len()+1).Tracef("Added event to send queue")
	q.Add(ev)

	if s.metrics != nil {
		s.metrics.ApplicationDeleted.Inc()
	}
}

// newAppProjectCallback is executed when a new AppProject event was emitted from
// the informer and needs to be sent out to an agent. If the receiving agent
// is in autonomous mode, this event will be discarded.
func (s *Server) newAppProjectCallback(outbound *v1alpha1.AppProject) {
	logCtx := log().WithFields(logrus.Fields{
		"component":       "EventCallback",
		"queue":           outbound.Namespace,
		"event":           "appproject_new",
		"appproject_name": outbound.Name,
	})

	s.resources.Add(outbound.Namespace, resources.NewResourceKeyFromAppProject(outbound))

	// Check if this AppProject was created by an autonomous agent by examining its name prefix
	if s.isAppProjectFromAutonomousAgent(outbound.Name) {
		logCtx.Debugf("Discarding event, because the appProject is managed by an autonomous agent")
		return
	}

	// Return early if no interested agent is connected
	if !s.queues.HasQueuePair(outbound.Namespace) {
		if err := s.queues.Create(outbound.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing namespace")
	}

	if s.metrics != nil {
		s.metrics.AppProjectCreated.Inc()
	}

	agents := s.mapAppProjectToAgents(*outbound)
	for agent := range agents {
		q := s.queues.SendQ(agent)
		if q == nil {
			logCtx.Errorf("Queue pair not found for agent %s", agent)
			continue
		}

		agentAppProject := AgentSpecificAppProject(*outbound, agent)
		ev := s.events.AppProjectEvent(event.Create, &agentAppProject)
		q.Add(ev)
		logCtx.Tracef("Added appProject %s to send queue, total length now %d", outbound.Name, q.Len())
	}
}

func (s *Server) updateAppProjectCallback(old *v1alpha1.AppProject, new *v1alpha1.AppProject) {
	s.watchLock.Lock()
	defer s.watchLock.Unlock()
	logCtx := log().WithFields(logrus.Fields{
		"component":       "EventCallback",
		"queue":           old.Namespace,
		"event":           "appproject_update",
		"appproject_name": old.Name,
	})

	// Check if this AppProject was created by an autonomous agent by examining its name prefix
	if s.isAppProjectFromAutonomousAgent(new.Name) {
		logCtx.Debugf("Discarding event, because the appProject is managed by an autonomous agent")
		return
	}

	if len(new.Finalizers) > 0 && len(new.Finalizers) != len(old.Finalizers) {
		var err error
		tmp, err := s.projectManager.RemoveFinalizers(s.ctx, new)
		if err != nil {
			logCtx.WithError(err).Warnf("Could not remove finalizer")
		} else {
			logCtx.Debug("Removed finalizer")
			new = tmp
		}
	}
	if s.projectManager.IsChangeIgnored(new.Name, new.ResourceVersion) {
		logCtx.WithField("resource_version", new.ResourceVersion).Debugf("Resource version has already been seen")
		return
	}
	if !s.queues.HasQueuePair(old.Namespace) {
		if err := s.queues.Create(old.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing agent namespace")
	}

	s.syncAppProjectUpdatesToAgents(old, new, logCtx)

	if s.metrics != nil {
		s.metrics.AppProjectUpdated.Inc()
	}
}

func (s *Server) deleteAppProjectCallback(outbound *v1alpha1.AppProject) {
	logCtx := log().WithFields(logrus.Fields{
		"component":       "EventCallback",
		"queue":           outbound.Namespace,
		"event":           "appproject_delete",
		"appproject_name": outbound.Name,
	})

	s.resources.Remove(outbound.Namespace, resources.NewResourceKeyFromAppProject(outbound))

	// Check if this AppProject was created by an autonomous agent by examining its name prefix
	if s.isAppProjectFromAutonomousAgent(outbound.Name) {
		logCtx.Debugf("Discarding event, because the appProject is managed by an autonomous agent")
		return
	}

	if !s.queues.HasQueuePair(outbound.Namespace) {
		if err := s.queues.Create(outbound.Namespace); err != nil {
			logCtx.WithError(err).Error("failed to create a queue pair for an existing agent namespace")
			return
		}
		logCtx.Trace("Created a new queue pair for the existing agent namespace")
	}
	q := s.queues.SendQ(outbound.Namespace)
	if q == nil {
		logCtx.Error("Help! Queue pair has disappeared!")
		return
	}

	agents := s.mapAppProjectToAgents(*outbound)
	for agent := range agents {
		q := s.queues.SendQ(agent)
		if q == nil {
			logCtx.Errorf("Queue pair not found for agent %s", agent)
			continue
		}

		agentAppProject := AgentSpecificAppProject(*outbound, agent)
		ev := s.events.AppProjectEvent(event.Delete, &agentAppProject)
		q.Add(ev)
		logCtx.WithField("sendq_len", q.Len()+1).Tracef("Added appProject delete event to send queue")
	}

	if s.metrics != nil {
		s.metrics.AppProjectDeleted.Inc()
	}
}

// deleteNamespaceCallback is called when the user deletes the agent namespace.
// Since there is no namespace we can remove the queue associated with this agent.
func (s *Server) deleteNamespaceCallback(outbound *corev1.Namespace) {
	logCtx := log().WithFields(logrus.Fields{
		"component":      "EventCallback",
		"queue":          outbound.Name,
		"event":          "namespace_delete",
		"namespace_name": outbound.Name,
	})

	if !s.queues.HasQueuePair(outbound.Name) {
		return
	}

	if err := s.queues.Delete(outbound.Name, true); err != nil {
		logCtx.WithError(err).Error("failed to remove the queue pair for a deleted agent namespace")
		return
	}

	// Remove eventwriter associated with this agent
	s.eventWriters.Remove(outbound.Name)

	logCtx.Tracef("Deleted the queue pair since the agent namespace is deleted")
}

// mapAppProjectToAgents maps an AppProject to the list of managed agents that should receive it.
// We sync an AppProject from the principal to an agent if:
// 1. It is a managed agent (not autonomous)
// 2. The agent name matches one of the AppProject's destinations and source namespaces
func (s *Server) mapAppProjectToAgents(appProject v1alpha1.AppProject) map[string]bool {
	agents := map[string]bool{}
	s.clientLock.RLock()
	defer s.clientLock.RUnlock()
	for agentName, mode := range s.namespaceMap {
		if mode != types.AgentModeManaged {
			continue
		}

		matched := false
		for _, dst := range appProject.Spec.Destinations {
			if glob.Match(dst.Name, agentName) {
				matched = true
				break
			}
		}

		if matched && glob.MatchStringInList(appProject.Spec.SourceNamespaces, agentName, glob.REGEXP) {
			agents[agentName] = true
		}
	}

	return agents
}

// syncAppProjectUpdatesToAgents sends the AppProject update events to the relevant clusters.
// It sends delete events to the clusters that no longer match the given AppProject.
func (s *Server) syncAppProjectUpdatesToAgents(old, new *v1alpha1.AppProject, logCtx *logrus.Entry) {
	oldAgents := s.mapAppProjectToAgents(*old)
	newAgents := s.mapAppProjectToAgents(*new)

	deletedAgents := map[string]bool{}

	// Delete the appProject from clusters that no longer match the new appProject
	for agent := range oldAgents {
		if _, ok := newAgents[agent]; ok {
			continue
		}

		q := s.queues.SendQ(agent)
		if q == nil {
			logCtx.Errorf("Queue pair not found for agent %s", agent)
			continue
		}

		agentAppProject := AgentSpecificAppProject(*new, agent)
		ev := s.events.AppProjectEvent(event.Delete, &agentAppProject)
		q.Add(ev)
		logCtx.Tracef("Sent a delete event for an AppProject for a removed cluster")
		deletedAgents[agent] = true
	}

	// Update the appProjects in the existing clusters
	for agent := range newAgents {
		if _, ok := deletedAgents[agent]; ok {
			continue
		}

		q := s.queues.SendQ(agent)
		if q == nil {
			logCtx.Errorf("Queue pair not found for agent %s", agent)
			continue
		}

		agentAppProject := AgentSpecificAppProject(*new, agent)
		ev := s.events.AppProjectEvent(event.SpecUpdate, &agentAppProject)
		q.Add(ev)
		logCtx.Tracef("Added appProject %s update event to send queue", new.Name)
	}
}

// AgentSpecificAppProject returns an agent specific version of the given AppProject
func AgentSpecificAppProject(appProject v1alpha1.AppProject, agent string) v1alpha1.AppProject {
	// Only keep the destinations that are relevant to the given agent
	filteredDst := []v1alpha1.ApplicationDestination{}
	for _, dst := range appProject.Spec.Destinations {
		if glob.Match(dst.Name, agent) {
			dst.Name = "in-cluster"
			dst.Server = "https://kubernetes.default.svc"

			filteredDst = append(filteredDst, dst)
		}
	}
	appProject.Spec.Destinations = filteredDst

	// Only allow Applications to be managed from the agent namespace
	appProject.Spec.SourceNamespaces = []string{agent}

	// Remove the roles since they are not relevant on the workload cluster
	appProject.Spec.Roles = []v1alpha1.ProjectRole{}

	return appProject
}

// isAppProjectFromAutonomousAgent checks if an AppProject was created by an autonomous agent
// by examining if its name is prefixed with an autonomous agent name (pattern: "{agentName}-{projectName}")
func (s *Server) isAppProjectFromAutonomousAgent(projectName string) bool {
	for agentName, mode := range s.namespaceMap {
		if mode == types.AgentModeAutonomous &&
			strings.HasPrefix(projectName, agentName+"-") {
			return true
		}
	}

	return false
}
