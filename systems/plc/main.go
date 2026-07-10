// Mock did:plc directory for local development.
//
// Stand-in for https://plc.directory so services (hilt) can publish tenant
// did:plc genesis/update/tombstone operations without touching the public
// directory. Stores the raw operation log per DID in memory and echoes it
// back verbatim, so it can never corrupt an operation by re-encoding it.
//
// Deliberate non-goals: no signature verification, no rotation-key checks,
// no prev-CID chain validation. It trusts whatever well-formed operations it
// receives.
//
// Endpoints (URL shapes match ucantone's did/plc DirectoryClient/Resolver):
//
//	GET  /health          - liveness probe
//	POST /{did}           - publish an operation or tombstone (dag-json body)
//	GET  /{did}/log/last  - last published operation, byte-for-byte
//	GET  /{did}           - DID document derived from the last operation
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
)

const (
	operationType = "plc_operation"
	tombstoneType = "plc_tombstone"
)

// didLog is the append-only operation log for a single did:plc.
type didLog struct {
	ops         [][]byte
	deactivated bool
}

type directory struct {
	mu   sync.Mutex
	dids map[string]*didLog
}

func newDirectory() *directory {
	return &directory{dids: map[string]*didLog{}}
}

// operation is the subset of a PLC operation the mock inspects. Raw bytes are
// what get stored and served; this is only for routing and document building.
type operation struct {
	Type                string            `json:"type"`
	VerificationMethods map[string]string `json:"verificationMethods"`
}

func (d *directory) handlePost(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("did")
	if !strings.HasPrefix(id, "did:plc:") {
		http.Error(w, "not a did:plc identifier", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "reading body", http.StatusBadRequest)
		return
	}
	var op operation
	if err := json.Unmarshal(body, &op); err != nil {
		http.Error(w, fmt.Sprintf("decoding operation: %v", err), http.StatusBadRequest)
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	dlog := d.dids[id]

	switch op.Type {
	case operationType:
		if dlog != nil && dlog.deactivated {
			http.Error(w, "DID has been deactivated", http.StatusBadRequest)
			return
		}
		if dlog == nil {
			dlog = &didLog{}
			d.dids[id] = dlog
		}
		dlog.ops = append(dlog.ops, body)
	case tombstoneType:
		if dlog == nil || len(dlog.ops) == 0 {
			http.Error(w, "cannot tombstone unknown DID", http.StatusBadRequest)
			return
		}
		if dlog.deactivated {
			http.Error(w, "DID has been deactivated", http.StatusBadRequest)
			return
		}
		dlog.ops = append(dlog.ops, body)
		dlog.deactivated = true
	default:
		http.Error(w, fmt.Sprintf("unknown operation type: %q", op.Type), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (d *directory) handleLast(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("did")
	d.mu.Lock()
	dlog := d.dids[id]
	var last []byte
	if dlog != nil && len(dlog.ops) > 0 {
		last = dlog.ops[len(dlog.ops)-1]
	}
	d.mu.Unlock()
	if last == nil {
		http.Error(w, "unknown DID", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(last)
}

// handleResolve serves a DID document built from the verification methods of
// the DID's last operation. The values are did:key strings; their multibase
// identifier doubles as the Multikey publicKeyMultibase.
func (d *directory) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("did")
	d.mu.Lock()
	dlog := d.dids[id]
	var last []byte
	if dlog != nil && !dlog.deactivated && len(dlog.ops) > 0 {
		last = dlog.ops[len(dlog.ops)-1]
	}
	d.mu.Unlock()
	if last == nil {
		http.Error(w, "unknown DID", http.StatusNotFound)
		return
	}
	var op operation
	if err := json.Unmarshal(last, &op); err != nil {
		http.Error(w, "decoding stored operation", http.StatusInternalServerError)
		return
	}

	type verificationMethod struct {
		ID                 string `json:"id"`
		Type               string `json:"type"`
		Controller         string `json:"controller"`
		PublicKeyMultibase string `json:"publicKeyMultibase"`
	}
	var methods []verificationMethod
	var relationships []string
	for name, keyDID := range op.VerificationMethods {
		methods = append(methods, verificationMethod{
			ID:                 id + "#" + name,
			Type:               "Multikey",
			Controller:         id,
			PublicKeyMultibase: strings.TrimPrefix(keyDID, "did:key:"),
		})
		relationships = append(relationships, id+"#"+name)
	}
	doc := map[string]any{
		"@context": []string{
			"https://www.w3.org/ns/did/v1",
			"https://w3id.org/security/multikey/v1",
		},
		"id":                   id,
		"verificationMethod":   methods,
		"capabilityInvocation": relationships,
		"capabilityDelegation": relationships,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(doc)
}

// logged wraps a handler with a stdout access log line.
func logged(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s", r.Method, r.URL.Path)
		next(w, r)
	}
}

func main() {
	addr := flag.String("addr", ":80", "listen address")
	flag.Parse()

	d := newDirectory()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("POST /{did}", logged(d.handlePost))
	mux.HandleFunc("GET /{did}/log/last", logged(d.handleLast))
	mux.HandleFunc("GET /{did}", logged(d.handleResolve))

	log.Printf("mock did:plc directory listening on %s", *addr)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
