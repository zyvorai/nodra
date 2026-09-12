---
hero:
  eyebrow: EDGE RUNTIME & CONTROL PLANE
  title: Nodra
  lead: >-
    The open edge runtime that keeps sites running when the cloud doesn't:
    local MQTT/HTTP ingress, a durable store-and-forward WAL, device twins,
    and edge app reconciliation — offline-first by design.
  swatches:
    - {label: "MQTT 3.1.1"}
    - {label: "HTTP ingress"}
    - {label: "Modbus TCP/RTU"}
    - {label: "Apache-2.0"}
  highlights:
    - {value: "v0.2.1", label: "Current release — adds Modbus RTU and J1939 industrial transports", footnote: "1"}
    - {value: "26", label: "Lettered fleet sites simulated end-to-end by nodra-sim", footnote: "2"}
    - {value: "2,000", label: "Activity entries retained live in the console Logs ring", footnote: "3"}
    - {value: "65532", label: "Non-root UID/GID enforced by the default Kubernetes manifests", footnote: "4"}
    - {value: "6", label: "Documented trust boundaries across edge, control plane, and console", footnote: "5"}
  hub_bands:
    - {icon: "❓", title: "FAQ", description: "Questions people evaluating Nodra actually ask, before they've decided to adopt it.", href: "FAQ.md"}
    - {icon: "🛠️", title: "Troubleshooting", description: "Real operational issues, with the documented fix — not a generic checklist.", href: "TROUBLESHOOTING.md"}
    - {icon: "▶️", title: "Demo and user walkthrough", description: "A full user-demo path: control plane console, A–Z fleet simulation, and verification scripts.", href: "DEMO.md"}
    - {icon: "🧩", title: "Architecture", description: "nodra-server is the management/control plane, nodrad is the edge runtime, nodractl is the operator CLI.", href: "ARCHITECTURE.md"}
    - {icon: "🔌", title: "API guide", description: "Base path /api/v1 — admin bearer auth for management, site tokens or mTLS for agents.", href: "API.md"}
footnotes:
  - {marker: "1", text: "v0.2.1 adds Modbus RTU and J1939 industrial transports on top of the v0.2.0 base.", href: "INDUSTRIAL_PROTOCOLS.md", href_label: "See Industrial protocols."}
  - {marker: "2", text: "nodra-sim seeds and continuously drives up to 26 lettered demo sites (A–Z).", href: "DEMO.md", href_label: "See the Demo walkthrough."}
  - {marker: "3", text: "Console activity is an in-memory ring capped at 2000 entries, intentionally non-durable.", href: "ARCHITECTURE.md#durability", href_label: "See Architecture — Activity log."}
  - {marker: "4", text: "Default manifests run as non-root UID/GID 65532 with a read-only root filesystem and all capabilities dropped.", href: "SECURITY-MODEL.md#kubernetes", href_label: "See Security model — Kubernetes."}
  - {marker: "5", text: "Six trust boundaries are documented: device→nodrad, nodrad→control plane, operator→API, operator→console, control plane→webhook, and simulator→control plane.", href: "SECURITY-MODEL.md#trust-boundaries", href_label: "See Security model — Trust boundaries."}
---

Nodra is an Apache-2.0 edge runtime and control plane from Zyvor. It gives
remote sites a local MQTT/HTTP ingress, durable store-and-forward, local
routes, device twins, edge application reconciliation, fleet health,
replayable dead letters and a clean web control plane. Edge sites are
offline-first: WAN loss is a first-class operating mode, not a degraded one.

For the full project overview — architecture diagram, feature highlights,
quick start, build instructions and licensing — see the
[README on GitHub](https://github.com/zyvorai/nodra#readme).

## What's included today

<div class="icon-badge-list" markdown="1">

- 📡 MQTT 3.1.1 edge ingress
- 🌐 HTTP ingress
- 💾 Offline-first WAL spool
- 🔀 Local routes
- 👥 Device twins
- ✉️ Dead-letter queue with replay
- 🐳 Docker app reconciliation
- ☸️ Kubernetes-ready (Helm, Kustomize)

</div>

Source code, releases and issue tracking live in the
[zyvorai/nodra](https://github.com/zyvorai/nodra) GitHub repository.
