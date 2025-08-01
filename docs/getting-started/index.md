# Getting started

!!! warning
    The argocd-agent project is currently rather complex to set-up and operate. Right now, tt is not for the faint of heart. As the project progresses and hopefully gains more contributors, we will come up with means to smoothen both, the initial installation and day 2 operations.

## Preface

Installing and getting *argocd-agent* running involves a few things right now. A broad overview of the tasks at hand:

* Creating and maintaining a TLS certificate authority (CA) on your cluster. You can also use an existing CA, if you have one.
* Installation and configuration of parts of Argo CD on the central [control plane cluster](../concepts/components-terminology.md#control-plane-cluster), as well as on each [workload cluster](../concepts/components-terminology.md#workload-cluster)
* Installation and configuration of *argocd-agent*'s [principal](../concepts/components-terminology.md#principal) component on the control plane cluster
* Installation and configuration of *argocd-agent*'s [agent](../concepts/components-terminology.md#agent) component on each workload cluster
* Issuing TLS server certificates for the principal
* Issuing TLS client certificates for each agent

Some or all of these tasks may already be automated, depending on which platform(s) you will be working with.

## Choosing the proper name for each agent

Each agent must have a unique name assigned to it, which will stay the same over the agent's span of life.

When choosing a name, consider the following:

* Naming rules for an agent are equal to [naming rules for Kubernetes namespaces](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-label-names), and must follow the [DNS label standard](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#dns-label-names).
* The name of an agent should clearly identify the agent, and the cluster it is running on. The name of an agent will be visible as the destination cluster in Argo CD.
* For each agent, a namespace with the same name will be created on the control plane cluster. These namespaces must be accessible by the Argo CD API server on the control plane through apps-in-any-namespace configuration.
* The name of the agent must be part of its TLS client certificate's subject

## Choosing the right operational mode for each agent

As described in the chapter about [operational modes of agents](../concepts/agent-modes/index.md), each agent can operate in one of several modes. Each mode comes with its own pros and cons, and you want to consider the right mode for each agent.

It's perfectly fine to run a mixed-mode scenario, where some of the agents run in one mode while other agents run in different modes.

If in doubt, it's recommended to start using the [managed mode](../concepts/agent-modes/managed.md) for your agents.

## Requirements

### Clusters

You will need admin-level access to at least two Kubernetes clusters: One cluster for hosting the [control plane components](../concepts/components-terminology.md#control-plane-cluster), and one or more clusters to host the [workload cluster components](../concepts/components-terminology.md#workload-cluster).

You must be able to expose certain services on the control plane cluster, such as the principal's gRPC service, to be reachable by components on the workload cluster.

!!!hint
    You can use the awesome [vcluster](https://github.com/loft-sh/vcluster) project to partition an existing cluster (such as a microk8s running on your laptop) into multiple, fully isolated clusters. We do this with our official development and end-to-end testing environments, too!

### PKI

The *argocd-agent* components make use of mTLS for validating authenticity and identity of its peers. This requires the use client certificates, and the certificate of the CA needs to be known by each peer.

If you have a PKI already established, you can use it issue client certificates for each of your agents, as long as you make its identity known to all components.

For testing purposes, we deliver a CLI tool ("*argocd-agentctl*") that can setup a bare-bones PKI for you, and makes it easy to issue the correct certificates to the right places. This tool is highly experimental, and it should **under no circumstances** be used for production purposes.