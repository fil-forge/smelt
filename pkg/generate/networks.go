package generate

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"

	"github.com/fil-forge/smelt/pkg/manifest"
)

// This file emits the storage networks: one hilt per network and one ingot
// per region it serves, each with the identity, database, vault and ports
// that belong to it alone. Why each setting is what it is lives in
// systems/hilt/README.md and systems/ingot/README.md.
//
// A service's compose name is its did:web label and its DNS name on
// forge-network, so a peer resolves did:web:<name> straight from the
// container. Hilt's provider registration, the hilt -> ingot proof and the
// gateway's own config all name the same literal.

// ProofName is the file in generated/proofs holding the delegation the
// upload service issues to a hilt for /customer/add.
func ProofName(hilt manifest.ResolvedHilt) string {
	return hilt.Name + "-customer-add-proof.txt"
}

// S3ProofName is the file in generated/proofs holding the delegation a hilt
// issues to one of its gateways for the /s3/* commands.
func S3ProofName(hilt, ingot string) string {
	return hilt + "-" + ingot + "-s3-proof.txt"
}

// GenerateHiltCompose builds the compose file for every declared hilt: the
// service, its provider registrar, its vault and the vault's initializer,
// plus the one postgres server they share.
func GenerateHiltCompose(hilts []manifest.ResolvedHilt) ([]byte, error) {
	compose := newComposeFile()

	var databases []string
	for _, h := range hilts {
		compose.Services[h.Name] = buildHiltService(h)
		compose.Services[h.Name+"-init"] = buildHiltInitService(h)
		compose.Services[h.Vault()] = buildHiltVaultService(h)
		compose.Services[h.Vault()+"-init"] = buildHiltVaultInitService(h)
		compose.Volumes[h.Vault()+"-data"] = nil
		compose.Volumes[h.Vault()+"-init"] = nil
		if h.Index > 0 {
			databases = append(databases, h.Database())
		}
	}

	compose.Services["hilt-postgres"] = buildSharedPostgresService("hilt", 15111)
	compose.Volumes["hilt-postgres-data"] = nil
	if len(databases) > 0 {
		compose.Services["hilt-postgres-init"] = buildDatabaseInitService("hilt", databases)
	}

	return marshalCompose(compose)
}

// GenerateIngotCompose builds the compose file for every declared gateway:
// the service, its regional vault and the vault's initializer, plus the one
// postgres server they share.
func GenerateIngotCompose(ingots []manifest.ResolvedIngot, piri []manifest.ResolvedPiriNode) ([]byte, error) {
	compose := newComposeFile()

	var databases []string
	for _, i := range ingots {
		compose.Services[i.Name] = buildIngotService(i, piri)
		compose.Services[i.Vault()] = buildIngotVaultService(i)
		compose.Services[i.Vault()+"-init"] = buildIngotVaultInitService(i)
		compose.Volumes[i.Name+"-data"] = nil
		compose.Volumes[i.Vault()+"-data"] = nil
		compose.Volumes[i.Vault()+"-init"] = nil
		if i.Index > 0 {
			databases = append(databases, i.Database())
		}
	}

	compose.Services["ingot-postgres"] = buildSharedPostgresService("ingot", 15131)
	compose.Volumes["ingot-postgres-data"] = nil
	if len(databases) > 0 {
		compose.Services["ingot-postgres-init"] = buildDatabaseInitService("ingot", databases)
	}

	return marshalCompose(compose)
}

func buildHiltService(h manifest.ResolvedHilt) ComposeService {
	image := "${HILT_IMAGE:-ghcr.io/fil-forge/hilt:main}"
	if h.Image != "" {
		image = h.Image
	}
	depends := map[string]DependsOnCondition{
		"upload":            {Condition: "service_healthy"},
		"plc":               {Condition: "service_healthy"},
		"swarf":             {Condition: "service_healthy"},
		"hilt-postgres":     {Condition: "service_healthy"},
		h.Vault() + "-init": {Condition: "service_completed_successfully"},
	}
	// The leading hilt's database is the one POSTGRES_DB creates; every
	// other waits for the sidecar that creates its own.
	if h.Index > 0 {
		depends["hilt-postgres-init"] = DependsOnCondition{Condition: "service_completed_successfully"}
	}

	return ComposeService{
		Image: image,
		User:  "0:0", // key files are 600
		Ports: []string{fmt.Sprintf("${SMELT_%s_PORT:-%d:80}", envName(h.Name), h.Port())},
		Environment: []string{
			"HILT_SERVER_HOST=0.0.0.0",
			"HILT_SERVER_PORT=80",
			"HILT_IDENTITY_KEY_FILE=/keys/hilt.pem",
			"HILT_IDENTITY_SERVICE_ID=" + h.DID(),
			"HILT_AUTH_PARTNER_KEY=${HILT_PARTNER_KEY:-dev-partner-key}",
			"HILT_STORAGE_TYPE=postgres",
			fmt.Sprintf("HILT_STORAGE_POSTGRES_DSN=postgres://hilt:hilt@hilt-postgres:5432/%s?sslmode=disable", h.Database()),
			"OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_ENDPOINT:-${OTEL_EXPORTER_OTLP_ENDPOINT:-}}",
			"OTEL_TRACES_SAMPLER_ARG",
			"OTEL_RESOURCE_ATTRIBUTES",
			"OTEL_METRIC_EXPORT_INTERVAL",
			"HILT_VAULT_TYPE=openbao",
			fmt.Sprintf("HILT_VAULT_OPENBAO_ADDRESS=http://%s:8200", h.Vault()),
			"HILT_VAULT_OPENBAO_AUTH_METHOD=token",
			"HILT_VAULT_OPENBAO_TOKEN=${HILT_VAULT_TOKEN:-dev-hilt-vault-token}",
			"HILT_PLC_DIRECTORY=http://plc:3000",
			"HILT_UPLOAD_SERVICE_ID=did:web:upload",
			"HILT_UPLOAD_SERVICE_URL=http://upload:80",
			"HILT_REVOCATION_SERVICE_ID=did:web:swarf",
			"HILT_REVOCATION_SERVICE_URL=http://swarf:80",
			// The product every tenant of this network is registered
			// under with sprue. One per hilt, so two networks do not
			// write over each other's customers.
			"HILT_UPLOAD_PRODUCT_ID=" + h.DID(),
			"HILT_UPLOAD_PROOFS=/proofs/" + ProofName(h),
			"HILT_SERVER_INSECURE_DID_RESOLUTION=true",
			"HILT_LOG_LEVEL=info",
		},
		Volumes: []string{
			fmt.Sprintf("../keys/%s.pem:/keys/hilt.pem:ro", h.Name),
			"../proofs:/proofs:ro",
		},
		DependsOn:   depends,
		Healthcheck: healthEndpoint(),
		Restart:     "unless-stopped",
		Networks:    []string{"forge-network"},
	}
}

// buildHiltInitService registers this hilt's gateways as the providers for
// the regions they serve. One registrar per hilt: a provider record belongs
// to the hilt that holds the region's tenants.
func buildHiltInitService(h manifest.ResolvedHilt) ComposeService {
	image := "${HILT_IMAGE:-ghcr.io/fil-forge/hilt:main}"
	if h.Image != "" {
		image = h.Image
	}
	depends := map[string]DependsOnCondition{
		h.Name: {Condition: "service_healthy"},
		// Registration pushes the piri node set to sprue as each
		// region's routing policy, and sprue only accepts nodes it has
		// registered.
		"upload-init": {Condition: "service_completed_successfully"},
	}
	return ComposeService{
		Image:      image,
		User:       "0:0", // key files are 600
		Entrypoint: []string{"sh", "/register-provider.sh"},
		Environment: []string{
			"HILT_SERVER_HOST=" + h.Name,
			"HILT_SERVER_PORT=80",
			"HILT_IDENTITY_KEY_FILE=/keys/hilt.pem",
			"HILT_IDENTITY_SERVICE_ID=" + h.DID(),
			"SMELT_HILT_NAME=" + h.Name,
			"INGOT_REGION=${INGOT_REGION:-us-west-1}",
		},
		Volumes: []string{
			fmt.Sprintf("../keys/%s.pem:/keys/hilt.pem:ro", h.Name),
			// The node DIDs and the topology only: this container needs
			// the identities, never the keys.
			"../keys/piri-nodes.did:/piri-nodes.did:ro",
			"../keys/" + NetworksFile + ":/networks.tsv:ro",
			"../../systems/hilt/register-provider.sh:/register-provider.sh:ro",
		},
		DependsOn: depends,
		Restart:   "no",
		Networks:  []string{"forge-network"},
	}
}

func buildHiltVaultService(h manifest.ResolvedHilt) ComposeService {
	return buildVaultService(h.Vault(), h.VaultPort(), envName(h.Name)+"_VAULT")
}

// VaultConfigName is the rendered server config for one vault. Each carries
// its own api_addr, cluster_addr and raft node_id, so they cannot share a
// file the way the rest of the vault setup does.
func VaultConfigName(vault string) string { return vault + ".hcl" }

func buildHiltVaultInitService(h manifest.ResolvedHilt) ComposeService {
	return ComposeService{
		Image:      "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		User:       "0:0", // the image runs as `openbao`; the init volume is root-owned
		Entrypoint: []string{"sh", "/init.sh"},
		Environment: []string{
			fmt.Sprintf("BAO_ADDR=http://%s:8200", h.Vault()),
			"HILT_VAULT_TOKEN=${HILT_VAULT_TOKEN:-dev-hilt-vault-token}",
		},
		Volumes: []string{
			"../../systems/hilt/openbao/init.sh:/init.sh:ro",
			"../../systems/common/openbao/bootstrap.sh:/openbao-bootstrap.sh:ro",
			h.Vault() + "-init:/init",
		},
		DependsOn: map[string]DependsOnCondition{
			h.Vault(): {Condition: "service_healthy"},
		},
		Restart:  "no",
		Networks: []string{"forge-network"},
	}
}

func buildIngotService(i manifest.ResolvedIngot, piri []manifest.ResolvedPiriNode) ComposeService {
	image := "${INGOT_IMAGE:-ghcr.io/fil-forge/ingot:main}"
	if i.Image != "" {
		image = i.Image
	}
	depends := map[string]DependsOnCondition{
		i.Vault() + "-init": {Condition: "service_completed_successfully"},
		i.Hilt:              {Condition: "service_healthy"},
		"plc":               {Condition: "service_healthy"},
		// Hilt only accepts /s3/* invocations from the provider
		// registered for the region, so wait for its registrar.
		i.Hilt + "-init": {Condition: "service_completed_successfully"},
		"upload-init":    {Condition: "service_completed_successfully"},
		"upload":         {Condition: "service_healthy"},
		"indexer":        {Condition: "service_healthy"},
		"swarf":          {Condition: "service_healthy"},
		"ingot-postgres": {Condition: "service_healthy"},
	}
	if len(piri) > 0 {
		depends[piri[0].Name] = DependsOnCondition{Condition: "service_healthy"}
	}
	if i.Index > 0 {
		depends["ingot-postgres-init"] = DependsOnCondition{Condition: "service_completed_successfully"}
	}

	return ComposeService{
		Image:   image,
		User:    "0:0", // key files are 600
		Command: []string{"serve", "--config", "/etc/ingot/config.yaml"},
		Ports:   []string{fmt.Sprintf("${SMELT_%s_PORT:-%d:80}", envName(i.Name), i.Port())},
		Volumes: []string{
			fmt.Sprintf("%s-data:/data", i.Name),
			fmt.Sprintf("../keys/%s.pem:/keys/ingot.pem:ro", i.Name),
			"../proofs:/proofs:ro",
			// The rendered per-gateway config, which carries its region,
			// identity, database and the hilt it answers to.
			fmt.Sprintf("../ingot/%s.yaml:/etc/ingot/config.yaml:ro", i.Name),
		},
		Environment: []string{
			"INGOT_REGION=" + regionValue(i),
			"INGOT_LOG_LEVEL=${INGOT_LOG_LEVEL:-info}",
			"OTEL_EXPORTER_OTLP_ENDPOINT=${OTEL_ENDPOINT:-${OTEL_EXPORTER_OTLP_ENDPOINT:-}}",
			"OTEL_TRACES_SAMPLER_ARG",
			"OTEL_RESOURCE_ATTRIBUTES",
			"OTEL_METRIC_EXPORT_INTERVAL",
			"INGOT_LOCAL_BLOB_MAX_BYTES",
			"INGOT_REGIONKEY_OPENBAO_TOKEN=${INGOT_OPENBAO_TOKEN:-dev-ingot-region-token}",
		},
		DependsOn:   depends,
		Healthcheck: healthEndpoint(),
		Restart:     "unless-stopped",
		Networks:    []string{"forge-network"},
	}
}

func buildIngotVaultService(i manifest.ResolvedIngot) ComposeService {
	return buildVaultService(i.Vault(), i.VaultPort(), envName(i.Name)+"_OPENBAO")
}

func buildIngotVaultInitService(i manifest.ResolvedIngot) ComposeService {
	return ComposeService{
		Image:      "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		User:       "0:0",
		Entrypoint: []string{"sh", "/init.sh"},
		Environment: []string{
			fmt.Sprintf("BAO_ADDR=http://%s:8200", i.Vault()),
			"INGOT_OPENBAO_TOKEN=${INGOT_OPENBAO_TOKEN:-dev-ingot-region-token}",
			"INGOT_REGION_KEK=region-kek",
		},
		Volumes: []string{
			"../../systems/ingot/openbao/init.sh:/init.sh:ro",
			"../../systems/common/openbao/bootstrap.sh:/openbao-bootstrap.sh:ro",
			i.Vault() + "-init:/init",
		},
		DependsOn: map[string]DependsOnCondition{
			i.Vault(): {Condition: "service_healthy"},
		},
		Restart:  "no",
		Networks: []string{"forge-network"},
	}
}

// buildVaultService is the OpenBao both services run: a non-dev server with
// raft storage on its own volume, so its keys survive down/up and snapshot
// restores. The healthcheck only means "listening"; a fresh server is
// uninitialized and a restarted one sealed until its init job runs again.
func buildVaultService(name string, port int, portEnv string) ComposeService {
	return ComposeService{
		Image:   "${OPENBAO_IMAGE:-openbao/openbao:2.6}",
		Command: []string{"server"},
		Ports:   []string{fmt.Sprintf("${SMELT_%s_PORT:-%d:8200}", portEnv, port)},
		Environment: []string{
			// For `docker compose exec <vault> bao ...` (the CLI defaults to https)
			"BAO_ADDR=http://127.0.0.1:8200",
		},
		Volumes: []string{
			fmt.Sprintf("../openbao/%s:/openbao/config/config.hcl:ro", VaultConfigName(name)),
			name + "-data:/openbao/file",
		},
		Healthcheck: &Healthcheck{
			Test:          []string{"CMD", "wget", "-q", "--spider", "http://127.0.0.1:8200/v1/sys/health?sealedcode=200&uninitcode=200"},
			StartInterval: "1s",
			Interval:      "10s",
			Timeout:       "5s",
			Retries:       5,
			StartPeriod:   "10s",
		},
		Restart:  "unless-stopped",
		Networks: []string{"forge-network"},
	}
}

// buildSharedPostgresService is the one server a kind of service shares.
// POSTGRES_DB creates the leading instance's database, so a stack that has
// only ever run one of them needs no init sidecar and keeps its volume.
func buildSharedPostgresService(kind string, port int) ComposeService {
	return ComposeService{
		Image: "${POSTGRES_IMAGE:-postgres:16-alpine}",
		Ports: []string{fmt.Sprintf("${SMELT_%s_POSTGRES_PORT:-%d:5432}", strings.ToUpper(kind), port)},
		Environment: []string{
			"POSTGRES_USER=" + kind,
			"POSTGRES_PASSWORD=" + kind,
			"POSTGRES_DB=" + kind,
		},
		Volumes: []string{
			fmt.Sprintf("%s-postgres-data:/var/lib/postgresql/data", kind),
		},
		Healthcheck: &Healthcheck{
			Test:          []string{"CMD-SHELL", fmt.Sprintf("pg_isready -h 127.0.0.1 -U %s -d %s", kind, kind)},
			StartInterval: "1s",
			Interval:      "5s",
			Timeout:       "3s",
			Retries:       10,
			StartPeriod:   "10s",
		},
		Restart:  "unless-stopped",
		Networks: []string{"forge-network"},
	}
}

// buildDatabaseInitService creates the databases the instances after the
// first need. Each service runs its own goose migrations once it connects,
// so creating an empty database is all this does.
func buildDatabaseInitService(kind string, databases []string) ComposeService {
	return ComposeService{
		Image:      "${POSTGRES_IMAGE:-postgres:16-alpine}",
		Entrypoint: []string{"sh", "/postgres-init.sh"},
		Environment: []string{
			"PGPASSWORD=" + kind,
			"SMELT_POSTGRES_HOST=" + kind + "-postgres",
			"SMELT_POSTGRES_USER=" + kind,
			"SMELT_POSTGRES_DATABASES=" + strings.Join(databases, " "),
		},
		Volumes: []string{
			"../../systems/common/postgres-init.sh:/postgres-init.sh:ro",
		},
		DependsOn: map[string]DependsOnCondition{
			kind + "-postgres": {Condition: "service_healthy"},
		},
		Restart:  "no",
		Networks: []string{"forge-network"},
	}
}

// healthEndpoint is the check both services answer: /health on their
// in-container port 80.
func healthEndpoint() *Healthcheck {
	return &Healthcheck{
		Test:          []string{"CMD", "curl", "-sf", "http://localhost:80/health"},
		StartInterval: "1s",
		Interval:      "10s",
		Timeout:       "5s",
		Retries:       5,
		StartPeriod:   "10s",
	}
}

// regionValue is the literal region a gateway serves, or the shell variable
// a single-gateway stack has always been pointed with.
func regionValue(i manifest.ResolvedIngot) string {
	if i.Region == "" {
		return "${INGOT_REGION:-us-west-1}"
	}
	return i.Region
}

// envName turns a service name into the SMELT_<NAME>_PORT fragment that
// overrides its host port, as pkg/stack does to let parallel test stacks
// take ephemeral ports.
func envName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// GenerateIngotConfig renders one gateway's config.yaml from the template in
// systems/ingot/config. Everything that differs between gateways is here:
// the region, the identity, the database, the hilt that authorizes it and
// the vault holding the region KEK.
func GenerateIngotConfig(tmpl string, i manifest.ResolvedIngot) ([]byte, error) {
	t, err := template.New("ingot-config").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("parse ingot config template: %w", err)
	}
	var buf bytes.Buffer
	err = t.Execute(&buf, struct {
		Name       string
		Region     string
		DID        string
		Database   string
		Hilt       string
		HiltDID    string
		Vault      string
		S3ProofTxt string
	}{
		Name: i.Name,
		// The template's region is only a default: compose always passes
		// INGOT_REGION, which wins. A gateway that leaves its region to
		// the shell therefore renders the same default as before.
		Region:     regionDefault(i),
		DID:        i.DID(),
		Database:   i.Database(),
		Hilt:       i.Hilt,
		HiltDID:    "did:web:" + i.Hilt,
		Vault:      i.Vault(),
		S3ProofTxt: S3ProofName(i.Hilt, i.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("render ingot config: %w", err)
	}
	return buf.Bytes(), nil
}

func regionDefault(i manifest.ResolvedIngot) string {
	if i.Region == "" {
		return "us-west-1"
	}
	return i.Region
}

// GenerateVaultConfig renders one vault's server config from a template in
// systems/<service>/openbao. Only the addresses and the raft node id differ
// between instances.
func GenerateVaultConfig(tmpl, vault string) ([]byte, error) {
	t, err := template.New("vault-config").Parse(tmpl)
	if err != nil {
		return nil, fmt.Errorf("parse vault config template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, struct{ Vault string }{Vault: vault}); err != nil {
		return nil, fmt.Errorf("render vault config: %w", err)
	}
	return buf.Bytes(), nil
}
