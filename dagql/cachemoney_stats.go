package dagql

type CachemoneyDebugStats struct {
	MaterializationOutcomesByRole   map[string]map[string]uint64        `json:"materialization_outcomes_by_role,omitempty"`
	RecomputeReasons                map[string]uint64                   `json:"recompute_reasons,omitempty"`
	HydrationFailures               map[string]uint64                   `json:"hydration_failures,omitempty"`
	ViabilityCensusByReason         map[string]CachemoneyViabilityCount `json:"viability_census_by_reason,omitempty"`
	BytesDownloaded                 uint64                              `json:"bytes_downloaded,omitempty"`
	BlobsDownloaded                 uint64                              `json:"blobs_downloaded,omitempty"`
	BlobsSkippedAlreadyPresent      uint64                              `json:"blobs_skipped_already_present,omitempty"`
	SnapshotsOffered                uint64                              `json:"snapshots_offered,omitempty"`
	SnapshotsRequested              uint64                              `json:"snapshots_requested,omitempty"`
	BlobsOffered                    uint64                              `json:"blobs_offered,omitempty"`
	BlobsUploadRequested            uint64                              `json:"blobs_upload_requested,omitempty"`
	BlobsUploaded                   uint64                              `json:"blobs_uploaded,omitempty"`
	BlobsUploadFailed               uint64                              `json:"blobs_upload_failed,omitempty"`
	BlobsUploadSkippedAlreadyExists uint64                              `json:"blobs_upload_skipped_already_exists,omitempty"`
	ExportsStarted                  uint64                              `json:"exports_started,omitempty"`
	ExportsCompleted                uint64                              `json:"exports_completed,omitempty"`
	ImportsStarted                  uint64                              `json:"imports_started,omitempty"`
	ImportsCompleted                uint64                              `json:"imports_completed,omitempty"`
}

type CachemoneyViabilityCount struct {
	Total      uint64 `json:"total,omitempty"`
	Viable     uint64 `json:"viable,omitempty"`
	Eligible   uint64 `json:"eligible,omitempty"`
	Ineligible uint64 `json:"ineligible,omitempty"`
}

const (
	CachemoneyMaterializationLocal                = "local"
	CachemoneyMaterializationHydrated             = "hydrated"
	CachemoneyMaterializationRecomputedRemoteMiss = "recomputed_remote_miss"
	CachemoneyMaterializationRecomputedNoPlan     = "recomputed_no_plan"
	CachemoneyMaterializationFailed               = "failed"

	CachemoneyRecomputeReasonIndexMiss         = "index_miss"
	CachemoneyRecomputeReasonFetchFailed       = "fetch_failed"
	CachemoneyRecomputeReasonImportFailed      = "import_failed"
	CachemoneyRecomputeReasonInvalidDescriptor = "invalid_descriptor"
	CachemoneyRecomputeReasonUnknown           = "unknown"
)

func (c *Cache) DebugCachemoneyStats() CachemoneyDebugStats {
	if c == nil {
		return CachemoneyDebugStats{}
	}
	c.cachemoneyMu.RLock()
	defer c.cachemoneyMu.RUnlock()
	return cloneCachemoneyDebugStats(c.cachemoneyStats)
}

func (c *Cache) recordCachemoneyMaterialization(role, outcome string) {
	if c == nil {
		return
	}
	if role == "" {
		role = "unknown"
	}
	if outcome == "" {
		outcome = CachemoneyMaterializationFailed
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	if c.cachemoneyStats.MaterializationOutcomesByRole == nil {
		c.cachemoneyStats.MaterializationOutcomesByRole = map[string]map[string]uint64{}
	}
	byOutcome := c.cachemoneyStats.MaterializationOutcomesByRole[role]
	if byOutcome == nil {
		byOutcome = map[string]uint64{}
		c.cachemoneyStats.MaterializationOutcomesByRole[role] = byOutcome
	}
	byOutcome[outcome]++
}

func (c *Cache) recordCachemoneyRecompute(reason string) {
	if c == nil {
		return
	}
	if reason == "" {
		reason = CachemoneyRecomputeReasonUnknown
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	if c.cachemoneyStats.RecomputeReasons == nil {
		c.cachemoneyStats.RecomputeReasons = map[string]uint64{}
	}
	c.cachemoneyStats.RecomputeReasons[reason]++
}

func (c *Cache) recordCachemoneyHydrationFailure(reason string) {
	if c == nil {
		return
	}
	if reason == "" {
		reason = CachemoneyRecomputeReasonUnknown
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	if c.cachemoneyStats.HydrationFailures == nil {
		c.cachemoneyStats.HydrationFailures = map[string]uint64{}
	}
	c.cachemoneyStats.HydrationFailures[reason]++
}

func (c *Cache) recordCachemoneyBlobSkippedAlreadyPresent() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.BlobsSkippedAlreadyPresent++
}

func (c *Cache) recordCachemoneyBlobDownloaded(bytesDownloaded uint64) {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.BlobsDownloaded++
	c.cachemoneyStats.BytesDownloaded += bytesDownloaded
}

func (c *Cache) recordCachemoneyExportStarted() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.ExportsStarted++
}

func (c *Cache) recordCachemoneyExportOffer(snapshots, blobs int) {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.SnapshotsOffered += uint64(snapshots)
	c.cachemoneyStats.BlobsOffered += uint64(blobs)
}

func (c *Cache) recordCachemoneyExportBeginAccepted(snapshots, requestedBlobs int) {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.SnapshotsRequested += uint64(snapshots)
	c.cachemoneyStats.BlobsUploadRequested += uint64(requestedBlobs)
}

func (c *Cache) recordCachemoneyBlobUpload(alreadyExists bool) {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	if alreadyExists {
		c.cachemoneyStats.BlobsUploadSkippedAlreadyExists++
	} else {
		c.cachemoneyStats.BlobsUploaded++
	}
}

func (c *Cache) recordCachemoneyBlobUploadFailed() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.BlobsUploadFailed++
}

func (c *Cache) recordCachemoneyExportCompleted() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.ExportsCompleted++
}

func (c *Cache) recordCachemoneyImportStarted() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.ImportsStarted++
}

func (c *Cache) recordCachemoneyImportCompleted() {
	if c == nil {
		return
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	c.cachemoneyStats.ImportsCompleted++
}

func (c *Cache) recordCachemoneyViability(reason string, viable, eligible bool) {
	if c == nil {
		return
	}
	if reason == "" {
		reason = "unknown"
	}
	c.cachemoneyMu.Lock()
	defer c.cachemoneyMu.Unlock()
	if c.cachemoneyStats.ViabilityCensusByReason == nil {
		c.cachemoneyStats.ViabilityCensusByReason = map[string]CachemoneyViabilityCount{}
	}
	count := c.cachemoneyStats.ViabilityCensusByReason[reason]
	count.Total++
	if viable {
		count.Viable++
	}
	if eligible {
		count.Eligible++
	} else {
		count.Ineligible++
	}
	c.cachemoneyStats.ViabilityCensusByReason[reason] = count
}

func cloneCachemoneyDebugStats(in CachemoneyDebugStats) CachemoneyDebugStats {
	out := in
	out.MaterializationOutcomesByRole = cloneNestedUint64Map(in.MaterializationOutcomesByRole)
	out.RecomputeReasons = cloneUint64Map(in.RecomputeReasons)
	out.HydrationFailures = cloneUint64Map(in.HydrationFailures)
	if len(in.ViabilityCensusByReason) > 0 {
		out.ViabilityCensusByReason = make(map[string]CachemoneyViabilityCount, len(in.ViabilityCensusByReason))
		for reason, count := range in.ViabilityCensusByReason {
			out.ViabilityCensusByReason[reason] = count
		}
	}
	return out
}

func cloneNestedUint64Map(in map[string]map[string]uint64) map[string]map[string]uint64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]uint64, len(in))
	for outer, inner := range in {
		out[outer] = cloneUint64Map(inner)
	}
	return out
}

func cloneUint64Map(in map[string]uint64) map[string]uint64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]uint64, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
