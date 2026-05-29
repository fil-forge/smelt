package guppy

// The structs below mirror the JSON shapes emitted by the guppy CLI's
// `--output json` mode (one struct per command result). They are deliberately
// plain scalars with json tags matching guppy's wire output — DIDs and CIDs are
// strings, not domain types — so decoding needs no DAG-JSON machinery and smelt
// keeps its dependency graph at libforge+ucantone.
//
// guppy owns the authoritative shapes; these are the consumer's view of that
// contract. The golden tests in this package pin the wire format so that any
// drift in guppy's output fails loudly here (and again in the e2e suite, which
// decodes real guppy output).

// LoginResult is the result of `guppy login`.
type LoginResult struct {
	Account            string `json:"account"`
	LoggedIn           bool   `json:"logged_in"`
	AlreadyLoggedIn    bool   `json:"already_logged_in"`
	ClaimedDelegations int    `json:"claimed_delegations"`
}

// WhoamiResult is the result of `guppy whoami`.
type WhoamiResult struct {
	DID string `json:"did"`
}

// VersionResult is the result of `guppy version`.
type VersionResult struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
	BuiltBy string `json:"built_by"`
}

// SpaceGenerateResult is the result of `guppy space generate`.
type SpaceGenerateResult struct {
	DID string `json:"did"`
}

// SpaceItem is one entry of `guppy space list`.
type SpaceItem struct {
	ID    string   `json:"id"`
	Names []string `json:"names,omitempty"`
}

// SpaceInfoResult is the result of `guppy space info`.
type SpaceInfoResult struct {
	Space     string   `json:"space"`
	Providers []string `json:"providers"`
}

// SourceAddResult is the result of `guppy upload source add`.
type SourceAddResult struct {
	OK       bool   `json:"ok"`
	Space    string `json:"space"`
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// SourceItem is one entry of `guppy upload source list`.
type SourceItem struct {
	SourceID string `json:"source_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

// UploadCompleted is one successfully completed upload from `guppy upload`.
type UploadCompleted struct {
	RootCID  string `json:"root_cid"`
	UploadID string `json:"upload_id"`
	SourceID string `json:"source_id"`
	Attempts int    `json:"attempts"`
}

// UploadFailed is one failed upload from `guppy upload`.
type UploadFailed struct {
	UploadID string `json:"upload_id"`
	SourceID string `json:"source_id"`
	Error    string `json:"error"`
	Attempts int    `json:"attempts"`
}

// UploadResult is the result of `guppy upload`.
type UploadResult struct {
	Completed []UploadCompleted `json:"completed"`
	Failed    []UploadFailed    `json:"failed"`
}

// UploadListItem is one entry of `guppy ls`.
type UploadListItem struct {
	Root   string   `json:"root"`
	Shards []string `json:"shards,omitempty"`
}

// BlobItem is one entry of `guppy blob ls`.
type BlobItem struct {
	Digest string `json:"digest"`
	Size   uint64 `json:"size"`
}

// AccountItem is one entry of `guppy account list`.
type AccountItem struct {
	ID string `json:"id"`
}

// RetrieveResult is the result of `guppy retrieve`.
type RetrieveResult struct {
	OK         bool   `json:"ok"`
	Space      string `json:"space"`
	CID        string `json:"cid"`
	Subpath    string `json:"subpath,omitempty"`
	OutputPath string `json:"output_path"`
	Directory  bool   `json:"directory"`
}
