# sprue (upload) config — STAGING TEMPLATE.
#
# Rendered with `op inject` into $FORGE_SECRETS_DIR/sprue-config.yaml and mounted
# at /etc/sprue/config.yaml. Only the Postgres password is a secret (op:// ref);
# everything else is literal. S3/MinIO credentials are supplied via the AWS SDK
# env vars (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY) set on the container.

deployment:
  environment: "staging"
  # Staging enforces payment plans (unlike dev, which bypasses them).
  allow_provision_without_payment_plan: false
  max_replicas: 3

server:
  host: "0.0.0.0"
  port: 80 # port 80 for did:web resolution; Caddy fronts TLS
  public_url: "https://sprue.staging.fil.one"

identity:
  key_file: "/keys/sprue.pem"
  service_did: "did:web:sprue.staging.fil.one"

# No indexer in staging — an empty endpoint disables it (per sprue config docs).
indexer:
  endpoint: ""
  did: ""

# No mailer in staging — "nop" drops outgoing mail (so email-based login is
# unavailable; see the runbook).
mailer:
  type: "nop"
  sender: "noreply@staging.fil.one"

storage:
  type: "postgres"
  postgres:
    dsn: "postgres://sprue:{{ op://Fil One/FilOne Forge Staging/sprue-postgres-password }}@postgres:5432/sprue?sslmode=disable"
    max_conns: 10
    min_conns: 0
  s3:
    endpoint: "http://minio:9000"
    region: "us-east-1"
    agent_message_bucket: "agent-message"
    delegation_bucket: "delegation"
    upload_shards_bucket: "upload-shards"

log:
  level: "info"
