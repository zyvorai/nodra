# Edge / IIoT runtime research

Survey date: 2026-11-23. Method: web search plus reading Nodra's `ROADMAP.md`,
`internal/` and `docs/`. Search results were often table-of-contents level;
claims below are limited to what those sources state, and gaps are marked.

## What the field does

| Project | What it does | Source |
|---------|--------------|--------|
| **EdgeX Foundry** | Microservice edge platform; the reference rules engine is LF Edge eKuiper, which runs SQL (or graph) rules over the EdgeX message bus (source, SQL, sink). | [EdgeX eKuiper docs](https://docs.edgexfoundry.org/4.0/microservices/support/eKuiper/Ch-eKuiper), [eKuiper](https://docs.emqx.com/en/kuiper/latest/) |
| **Eclipse Ditto** | Digital-twin framework: Things with features, policies for access control, optional WoT Thing Model integration (since 2.4.0) that defines expected data types and min/max restrictions. Older feature definitions validated against JSON schema. | [Ditto domain model](https://eclipse.dev/ditto/basic-overview.html), [feature definitions](https://eclipse.dev/ditto/2018-02-08-feature-definition.html), [WoT integration](https://eclipse.dev/ditto/basic-wot-integration.html) |
| **AWS IoT Greengrass** | Stream manager buffers to local storage with configurable bandwidth, timeout and behaviour while connected or disconnected. | [Greengrass streams](https://docs.aws.amazon.com/greengrass/v2/developerguide/manage-data-streams.html) |
| **Azure IoT Edge** | IoT Edge hub stores upstream messages while offline; devices and modules keep running indefinitely; message TTL is a desired property in the module twin. | [Offline capabilities](https://learn.microsoft.com/azure/iot-edge/offline-capabilities) |
| **KubeEdge / Akri** | KubeEdge DeviceTwin and Akri device discovery were not covered by the search results; only a research prototype pairing SPIFFE/SPIRE with KubeEdge was found. Treated as out of scope. | [FLUIDOS paper](https://isca.hmu.gr/wp-content/uploads/2024/09/paper_iotsms2024.pdf) |
| **EMQX / NanoMQ / Mosquitto** | MQTT 5 features (topic alias for small messages, shared subscriptions for load balancing, message expiry decremented on bridging); EMQX rule engine with jq; NanoMQ 0.11 added rule-engine republish and hot-updated rule SQL. | [EMQX MQTT 5](https://docs.emqx.com/en/emqx/latest/design/mqtt-advanced.html), [NanoMQ 0.11.0](https://github.com/emqx/nanomq/releases/tag/0.11.0) |
| **Sparkplug B** | MQTT payload/topic standard for IIoT: `spBv1.0/<group>/<type>/<node>[/<device>]`, NBIRTH/NDEATH/DBIRTH/DDEATH/DDATA, NDEATH sent as the MQTT will, and a `spBv1.0/STATE/<host>` topic for primary hosts. The normative spec PDF could not be read through search, so exact STATE wording was taken from vendor docs and should be re-checked. | [Tahu spec](https://www.eclipse.org/tahu/spec/Sparkplug%20Topic%20Namespace%20and%20State%20ManagementV2.2-with%20appendix%20B%20format%20-%20Eclipse.pdf), [ThingsBoard Sparkplug](https://thingsboard.io/docs/reference/sparkplug-api/), [GE collector](https://www.ge.com/digital/documentation/historian/version2024/c_historian_collector_about_mqtt_sparkplugb.html) |
| **SPIFFE/SPIRE** | Node and workload attestation; device identity is outside core SPIFFE and relies on extensions such as TPM DevID (IEEE 802.1AR). | [SPIRE concepts](https://spiffe.io/docs/latest/spire-about/spire-concepts/), [Unit 42](https://unit42.paloaltonetworks.com/kubernetes-spiffe-spire-identity-spoofing/) |
| **Uptane** | TUF-derived OTA security: offline-signed Image repository plus online Director that decides per-device installs; full-verification clients cross-check both; snapshot metadata is checked for rollback. | [Uptane standard](https://uptane.org/docs/2.1.0/standard/uptane-standard), [deployment guide](https://uptane.org/papers/V2.1.0_uptane_deploy.pdf) |
| **OPC UA PubSub, OpenTelemetry for IoT, edge ML gating** | Searches returned nothing usable. Not assessed; Nodra already has an OTLP/HTTP metrics exporter and an OPC UA client. | n/a |

## What Nodra already has

Offline WAL spool with caps and drop policies, MQTT 3.1.1 broker with QoS 0-2 and
persistent sessions, device twins, route filter/transform rules, CSR-based site
certificates with rotation, cosign-verify-before-pull, staged OTA canary
campaigns with health-gated rollback, multi-tenant orgs, OIDC, audit export,
OTLP metrics, Postgres fleet store.

## Gaps (before this change)

1. No Sparkplug awareness: `spBv1.0` protobuf payloads were wrapped as opaque strings, so birth/death certificates, aliases and sequence gaps were invisible.
2. Spool state was reported as current depth only; nothing answered "how long can this site stay offline".
3. Twins accepted any desired document (Ditto/WoT define types and ranges) and there was no way to see which devices had not converged, or to preview a change.
4. OTA has no repository-level signing (Uptane director/image split), metadata rollback counters, or device-held trust roots.
5. Device identity relies on enrollment tokens and CSRs, not hardware attestation (TPM DevID / SPIRE).
6. No MQTT 5 (topic alias, shared subscriptions, message expiry).
7. Local rules are stateless filters/transforms: no time windows, hysteresis, or windowed aggregation like eKuiper.
8. No edge ML inference gating.

## Ranked ideas

| Rank | Idea | Rationale | Decision |
|------|------|-----------|----------|
| 1 | Sparkplug B decoding with birth/death/seq tracking | Highest practical value for brownfield plants (Ignition and similar already publish it); fits the existing MQTT ingress; pure parsing, testable with hostile input; no new dependency | **Built** |
| 2 | Spool capacity forecast (time-to-full vs outage duration) | Directly serves the product's offline-first promise; none of the surveyed buffers (Greengrass streams, IoT Edge hub TTL) surface a projected fill time; small, deterministic, and cheap on constrained hardware | **Built** |
| 3 | Twin schema validation, drift and desired-state dry-run | Closes the Ditto/WoT gap with a strict JSON-Schema subset; makes OTA/config rollouts safer; reuses the twin model and Postgres docs unchanged | **Built** |
| 4 | Offline rule engine with time windows and hysteresis | eKuiper parity for the most common industrial need (alarm debouncing); needs per-rule state persisted across restarts to be honest | Deferred: state persistence design is larger than this change |
| 5 | Uptane-style two-repository OTA metadata with rollback counters | Strongest OTA security upgrade; large scope (key hierarchy, metadata formats, device-side verifier) | Deferred |
| 6 | MQTT 5 (topic alias, shared subscriptions, expiry) | Common broker feature; requires a new packet codec and property parsing in the broker | Deferred |
| 7 | Hardware-bound device identity (TPM DevID / SPIFFE) | Valuable for fleets, but depends on platform hardware and an attestation service | Deferred |
| 8 | Edge ML inference gating | No usable research results; unclear fit with a ~800 KB runtime | Rejected for now |
| - | Fleet-wide config diff | Partially delivered: per-twin `dry_run` diff; fleet-wide multi-device dry-run not built | Partial |

See [Edge intelligence](EDGE_INTELLIGENCE.md) for the delivered features.
