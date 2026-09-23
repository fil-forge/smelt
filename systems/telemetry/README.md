# Telemetry System

Optional observability stack for Smelt local development. Provides metrics collection and distributed tracing.

## Quick Start

```bash
# Start the stack, pointing ingot at the collector
INGOT_OTEL_ENDPOINT=http://otel-collector:4318 make up

# Start the telemetry services on the same forge-network
cd systems/telemetry && docker compose --profile telemetry up -d

# View dashboards and traces: http://localhost:3001
```

## Components

| Service        | Port      | Purpose                             |
|----------------|-----------|-------------------------------------|
| Grafana        | 3001      | Dashboard visualization             |
| Prometheus     | 9090      | Metrics storage                     |
| Tempo          | 3200      | Distributed tracing                 |
| OTEL Collector | 4317/4318 | Telemetry pipeline (OTLP gRPC/HTTP) |

## Architecture

```
Services (piri, ipni, upload, etc.)
         │
         ▼ OTLP
   ┌─────────────┐
   │OTEL Collector│
   └─────────────┘
         │
    ┌────┴────┐
    ▼         ▼
┌──────┐  ┌─────┐
│Prom. │  │Tempo│
└──────┘  └─────┘
    └────┬────┘
         ▼
   ┌─────────┐
   │ Grafana │
   └─────────┘
```

## Dashboards

Pre-configured dashboards are available in Grafana under the "Smelt" folder:

- **Smelt Overview**: System health, telemetry pipeline metrics, resource usage

## Adding Custom Dashboards

1. Create dashboard in Grafana UI
2. Export as JSON (Dashboard Settings > JSON Model > Copy)
3. Save to `config/grafana/dashboards/`
4. Dashboard will be auto-provisioned on restart

## Configuring Services

Ingot exports traces when `INGOT_OTEL_ENDPOINT` is set (see the quick start);
it is unset by default, so `make up` and the SDK test stacks export nothing.
In Grafana, traces are under Explore → Tempo, service `ingot`.

To configure another service:

```yaml
environment:
  - OTEL_EXPORTER_OTLP_ENDPOINT=http://otel-collector:4317
  - OTEL_SERVICE_NAME=my-service
```

## Endpoints

| Endpoint      | URL                   | Description                  |
|---------------|-----------------------|------------------------------|
| Grafana UI    | http://localhost:3001 | Dashboards (anonymous admin) |
| Prometheus UI | http://localhost:9090 | Metrics query                |
| Tempo API     | http://localhost:3200 | Trace query                  |
| OTLP gRPC     | localhost:4317        | Send telemetry (gRPC)        |
| OTLP HTTP     | localhost:4318        | Send telemetry (HTTP)        |

## Resource Usage

The telemetry stack adds approximately:
- 500MB-1GB RAM
- Minimal CPU when idle
- Disk usage grows with retention (48h default for traces)

## Cleanup

The telemetry services are their own Compose project (`telemetry`), so root
`make down` leaves them running. From `systems/telemetry`:

```bash
# Stop telemetry services
docker compose --profile telemetry down

# Stop them and delete stored metrics, traces and dashboards
docker compose --profile telemetry down -v
```
