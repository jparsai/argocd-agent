# argocd-agent

[![Integration tests](https://github.com/argoproj-labs/argocd-agent/actions/workflows/ci.yaml/badge.svg)](https://github.com/argoproj-labs/argocd-agent/actions/workflows/ci.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/argoproj-labs/argocd-agent)](https://goreportcard.com/report/github.com/argoproj-labs/argocd-agent)
[![codecov](https://codecov.io/gh/argoproj-labs/argocd-agent/graph/badge.svg?token=NP5UEU279Z)](https://codecov.io/gh/argoproj-labs/argocd-agent)


`argocd-agent` provides building blocks for implementing a distributed architecture with a central control plane for the popular GitOps tool
[Argo CD](https://github.com/argoproj/argo-cd). It allows to scale out Argo CD in many-cluster scenarios by moving compute intensive parts of Argo CD (application controller, repository server) to the workload clusters ("spokes"), while keeping the control and observe components (API and UI) in a central location (control plane, or "hub").

Some might refer to this architecture as "hub and spokes" or the "pull model" with a "single pane of glass".

## Status and current limitations

> [!IMPORTANT]
> **Important notice:** The `argocd-agent` project has just been born and is far from ready for general consumption. We decided to make the code available as early as possible to attract potential collaborators and contributors along the way, and to get input from the community as the project progresses.

You can check the
[issue tracker](https://github.com/argoproj-labs/argocd-agent/issues)
for things that we plan to work in the future, and the
[milestones](https://github.com/argoproj-labs/argocd-agent/milestones)
for when we plan to.

Going forward, things *will* change dramatically. Do not use unless you can cope with that.

As of now, the following hard limitations apply to `argocd-agent`:

* ~~Because `argocd-agent` makes extensive use of bidirectional streams, a HTTP/2 connection between the agents and the server is a hard requirement. None of the current RPC libaries (gRPC, connectrpc) support HTTP/1.x. If you have any forward or reverse proxies in between who do not support HTTP/2, many features of `argocd-agent` will not work.~~ https://github.com/argoproj-labs/argocd-agent/pull/190 introduced preliminary support for HTTP/1 via websockets.

`argocd-agent` is way from being feature complete. Things we need to figure out are, among others, retrieving and displaying pod logs, running resource actions and resource manipulation.

## Quickstart

See the [quickstart guide](docs/hack/quickstart.md) for more information.

## Hacking, testing, demoing

For a development and demo environment, we provide [some scripts and docs](https://github.com/argoproj-labs/argocd-agent/tree/main/hack/dev-env).

Please refer to the [Contributing](#contributing) section below for information on how to contribute.

## Compatibility

`argocd-agent` works with an out-of-the-box Argo CD. We aim to support the currently supported Argo CD versions.

We plan to publish container images for every architecture supported by Argo CD.

## Architecture

`argocd-agent` consists of two basic components, which resemble a client/server model:

* The *control plane* aka the *principle*, which runs on the control-plane that also hosts the Argo CD API server and some other requirements
* One or more *agents* running on the workload clusters

The *control plane* represents a central location that implements management and observability, e.g. the Argo CD API and UI components. However, no reconciliation of Applications happens on the control plane.

An *agent* is deployed to each workload cluster. These clusters, however, are not connected from the control plane like they would be in a classical Argo CD multi-cluster setup. Instead, a subset of Argo CD (at least the application-controller) is deployed to those clusters as well. The agent on the workload cluster will communicate with the principal on the control plane. Depending on its operational mode configuration, the role of the agent is to either:

* Submit status information from the Applications on the workload cluster back to the control plane,
* Receive updates to Application configuration from the control plane or
* A combination of above tasks

In all cases, it's the agent that initiates the connection to the control plane, and never the other way round.

The following diagram displays an architecture in maximum autonomy mode:

![Architectural diagram](docs/images/argocd-agent-autonomous-architecture.drawio.png "Architectural Diagram")

## Design principles

The following paragraphs describe the design principles upon which `argocd-agent` is built. All enhancements and contributions should follow those principles.

### A permanent network connection is neither expected nor required

It is understood that workload clusters can be everywhere: In your black-fibre connected data centres, across different cloud providers, in a car, on a ship, wherever. Not all these locations will have a permanent, reliable and low-latency network connection.

Thus, `argocd-agent` is designed around the assumption that the connection between workload clusters (agents) and the control plane is not always available, and that it might not be possible to keep up a stable, good performing network connection between the components. However, the system will benefit from a stable network connection with low latency.

### Workload clusters are and will stay autonomous

When the agent cannot reach the control plane, the workload cluster still will be able to perform its operations in an autonomous way. Depending on the agent's mode of operation (see [Architecture](#Architecture) above), cluster admins may still be able to perform configuration (i.e. manage applications, etc) but those changes will only take effect once the agent is connected again.

There are architectural variants in which a workload cluster will be dependent upon the availability of the control plane, for example when the workload cluster uses a repository server or Redis cache on the control plane. However, there will always be a variant where fully autonomous workload clusters are supported.

### The initiating component is always the agent, not the control plane

Connections are established in one direction only: from the agent to the control plane. Neither the control plane nor the agents need to know exact details about the topology of the system, as long as the agents know which control plane to connect to. In some parts of this doucmentation, we mention something called a _bi-directional stream_. This refers to a gRPC mechanisms where both parties may randomly transmit and receive data from its peer, all while the connection is established only in one direction.

### Security

The control plane component of `argocd-agent` provides a gRPC API over HTTPS/2. The connections to the API require mutual TLS and strong authentication. The agent won't need access to the control plane's Kubernetes API, and the control plane component has limited capabilities on the cluster it is running in. Thus, depending on the operational mode of the agents, there will be no single point of compromise - even in the case the control plane is compromised, the blast radius will be limited.

### Be lightweight by default but keep extensibility in mind

The `argocd-agent` should not impose any mandatory, heavy runtime dependencies or operational patterns. The hurdle of getting started should be as low as possible. The project should stay unencumbered of requirements such as persistant storage or relational databases by default. We are aware that at some point in time we may hit a scaling limit, especially when it comes to etcd and the Kubernetes API. Thus, major parts such as Application backends on the principal are designed to be pluggable, so users can contribute and use different kinds of backends according to their scalability requirements and operational preferences.

## Operational variants

`argocd-agent` can run in two distinct modes of operation: A *managed* mode and an *autonomous* mode. Both modes cater for different types of setups, and the control plane can handle a mixed-mode scenario where some of the agents run in managed mode, and others run in autonomous mode. However, an agent can only run in one of the modes. Having some parts on the agent's system in managed, and others in autonomous mode, is not supported.

### Managed mode

In *managed mode*, the agent receives all of its configuration from the control plane. For example, if you create a new Application on the control plane, the agent will pull this Application to its local Argo CD. Any local changes to this Application will be overwritten by the primary copy on the control plane.

The agent will submit status updates for managed Applications, such as sync status and health, back to the control plane.

### Autonomous mode

In *autonomous mode*, the agent will not create or delete Applications on the agent's system. Instead, it is expected that the system runs in self-managed mode (i.e. Argo CD completely configured from Git, and all changes are made through Git). The agent will sync the state of the system to the control plane, so every CRUD operation through Argo CD on Applications will be reflected on the control plane.

## Contributing

We are grateful for all contributions to this project, and everybody is welcome to contribute to it in various ways.

Participating in this project is subject to the [CNCF Code of Conduct](https://github.com/cncf/foundation/blob/main/code-of-conduct.md).

See [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md) for contribution guidlines specific to code, test, and docs.

Please note that at this point in time, code contributors are expected to hack with minimal guidance. We do not yet have any guidelines, the build & test process is bumpy, things will change at high speed. It's best to orientate yourself on existing code.

Please also note that code contributions will only be accepted if accompanied by proper unit tests, are passing the CI and are using signed-off commits to satisfy the DCO. No exceptions.

If you want to get in touch, please use GitHub's [discussions](https://github.com/argoproj-labs/argocd-agent/discussions) or [issue tracker](https://github.com/argoproj-labs/argocd-agent/issues) or join [#argo-cd-agent](https://cloud-native.slack.com/archives/C07L5SX6A9J) on the [CNCF Slack](https://slack.cncf.io/).
