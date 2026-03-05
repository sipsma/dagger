package persistdb

type Meta struct {
	Key   string
	Value string
}

type Result struct {
	ResultKey            string
	IDDigest             string
	OutputDigest         string
	OutputExtraDigests   string
	OutputEffectIDs      string
	SelfType             string
	SelfVersion          int64
	SelfPayload          []byte
	DepOfPersistedResult bool
	SafeToPersistCache   bool
	UnsafeMarker         bool
	PersistReason        string
	CreatedAtUnixNano    int64
	ExpiresAtUnix        int64
	RecordType           string
	Description          string
	Deleted              bool
	DeletedAtUnixNano    int64
}

type Term struct {
	TermDigest        string
	SelfDigest        string
	InputDigestsJSON  string
	CreatedAtUnixNano int64
	Deleted           bool
	DeletedAtUnixNano int64
}

type TermResult struct {
	TermDigest        string
	ResultKey         string
	Deleted           bool
	DeletedAtUnixNano int64
}

type Dep struct {
	ParentResultKey   string
	DepResultKey      string
	Deleted           bool
	DeletedAtUnixNano int64
}

type EqFact struct {
	OwnerResultKey    string
	LHSDigest         string
	RHSDigest         string
	CreatedAtUnixNano int64
	Deleted           bool
	DeletedAtUnixNano int64
}

type SnapshotRef struct {
	RefKey            string
	Kind              string
	SnapshotID        string
	SnapshotterName   string
	LeaseID           string
	ContentBlobDigest string
	ChainID           string
	BlobChainID       string
	DiffID            string
	MediaType         string
	BlobSize          int64
	URLsJSON          string
	RecordType        string
	Description       string
	CreatedAtUnixNano int64
	SizeBytes         int64
	Deleted           bool
	DeletedAtUnixNano int64
}

type ResultSnapshotRef struct {
	ResultKey         string
	RefKey            string
	Role              string
	Slot              string
	Deleted           bool
	DeletedAtUnixNano int64
}
