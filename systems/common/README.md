# Common System

Shared infrastructure services used by multiple systems.

## Services

- **minio** - Local S3-compatible storage
- **smtp4dev** - Local SMTP server with Web UI and REST API

## Ports

| Host Port | Container Port | Service | Description |
|-----------|----------------|---------|-------------|
| 15070 | 9000 | minio | S3 API endpoint |
| 15071 | 9001 | minio | Console endpoint |
| 15080 | 25   | email (smtp4dev) | SMTP endpoint |
| 15081 | 80   | email (smtp4dev) | Web UI / API |

## Standalone Usage

```bash
cd systems/common
docker compose up -d
```

## Dependencies

None - this is a foundational service.

## Used By

- upload (minio, email)
