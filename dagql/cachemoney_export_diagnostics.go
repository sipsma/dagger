package dagql

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

type cachemoneyExportDiagnosticObject struct {
	ResultID    sharedResultID
	TypeName    string
	Field       string
	RecordType  string
	Description string
	Error       string
}

type cachemoneyExportDiagnosticSnapshotLink struct {
	ResultID    sharedResultID
	TypeName    string
	Field       string
	Role        string
	RefKey      string
	RecordType  string
	Description string
}

type cachemoneyExportDiagnostics struct {
	UnpersistableObjects []cachemoneyExportDiagnosticObject
	ObjectEncodeErrors   []cachemoneyExportDiagnosticObject
	MutableSnapshotLinks []cachemoneyExportDiagnosticSnapshotLink
}

func (d cachemoneyExportDiagnostics) empty() bool {
	return len(d.UnpersistableObjects) == 0 &&
		len(d.ObjectEncodeErrors) == 0 &&
		len(d.MutableSnapshotLinks) == 0
}

func (d cachemoneyExportDiagnostics) String() string {
	if d.empty() {
		return ""
	}
	var b strings.Builder
	d.appendSummary(&b)
	if len(d.UnpersistableObjects) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "unpersistable object payloads (%d):", len(d.UnpersistableObjects))
		for _, obj := range d.UnpersistableObjects {
			fmt.Fprintf(&b, " result=%d type=%q", obj.ResultID, obj.TypeName)
			appendCachemoneyExportDiagnosticResultContext(&b, obj.Field, obj.RecordType, obj.Description)
			if obj.Error != "" {
				fmt.Fprintf(&b, " err=%q", obj.Error)
			}
			b.WriteString(";")
		}
	}
	if len(d.ObjectEncodeErrors) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "persisted object encode errors (%d):", len(d.ObjectEncodeErrors))
		for _, obj := range d.ObjectEncodeErrors {
			fmt.Fprintf(&b, " result=%d type=%q", obj.ResultID, obj.TypeName)
			appendCachemoneyExportDiagnosticResultContext(&b, obj.Field, obj.RecordType, obj.Description)
			if obj.Error != "" {
				fmt.Fprintf(&b, " err=%q", obj.Error)
			}
			b.WriteString(";")
		}
	}
	if len(d.MutableSnapshotLinks) > 0 {
		if b.Len() > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "mutable snapshot links (%d):", len(d.MutableSnapshotLinks))
		for _, link := range d.MutableSnapshotLinks {
			fmt.Fprintf(&b, " result=%d type=%q role=%q ref=%q", link.ResultID, link.TypeName, link.Role, link.RefKey)
			appendCachemoneyExportDiagnosticResultContext(&b, link.Field, link.RecordType, link.Description)
			b.WriteString(";")
		}
	}
	return strings.TrimSpace(b.String())
}

func (d cachemoneyExportDiagnostics) appendSummary(b *strings.Builder) {
	segments := make([]string, 0, 3)
	if len(d.UnpersistableObjects) > 0 {
		segments = append(segments, "unpersistable types: "+formatCachemoneyExportDiagnosticCounts(countCachemoneyExportDiagnosticObjects(d.UnpersistableObjects)))
	}
	if len(d.ObjectEncodeErrors) > 0 {
		segments = append(segments, "encode-error types: "+formatCachemoneyExportDiagnosticCounts(countCachemoneyExportDiagnosticObjects(d.ObjectEncodeErrors)))
	}
	if len(d.MutableSnapshotLinks) > 0 {
		segments = append(segments, "mutable links: "+formatCachemoneyExportDiagnosticCounts(countCachemoneyExportDiagnosticSnapshotLinks(d.MutableSnapshotLinks)))
	}
	if len(segments) == 0 {
		return
	}
	fmt.Fprintf(b, "summary: %s.", strings.Join(segments, "; "))
}

func countCachemoneyExportDiagnosticObjects(objects []cachemoneyExportDiagnosticObject) map[string]int {
	counts := make(map[string]int, len(objects))
	for _, obj := range objects {
		counts[obj.TypeName]++
	}
	return counts
}

func countCachemoneyExportDiagnosticSnapshotLinks(links []cachemoneyExportDiagnosticSnapshotLink) map[string]int {
	counts := make(map[string]int, len(links))
	for _, link := range links {
		key := link.TypeName + "/" + link.Role
		if link.RecordType != "" {
			key += "/" + link.RecordType
		}
		counts[key]++
	}
	return counts
}

func formatCachemoneyExportDiagnosticCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, counts[key]))
	}
	return strings.Join(parts, ", ")
}

func appendCachemoneyExportDiagnosticResultContext(b *strings.Builder, field, recordType, description string) {
	if field != "" {
		fmt.Fprintf(b, " field=%q", field)
	}
	if recordType != "" {
		fmt.Fprintf(b, " recordType=%q", recordType)
	}
	if description != "" {
		fmt.Fprintf(b, " description=%q", description)
	}
}

func (c *Cache) annotateCachemoneyExportError(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}
	diagnostics := c.cachemoneyExportDiagnostics(ctx)
	if diagnostics.empty() {
		return err
	}
	return fmt.Errorf("%w; cachemoney export diagnostics: %s", err, diagnostics.String())
}

func (c *Cache) cachemoneyExportDiagnostics(ctx context.Context) cachemoneyExportDiagnostics {
	if c == nil {
		return cachemoneyExportDiagnostics{}
	}

	type diagnosticResult struct {
		id          sharedResultID
		res         *sharedResult
		recordType  string
		description string
	}

	c.egraphMu.RLock()
	resultIDs := make([]sharedResultID, 0, len(c.resultsByID))
	for resultID := range c.resultsByID {
		resultIDs = append(resultIDs, resultID)
	}
	slices.Sort(resultIDs)
	results := make([]diagnosticResult, 0, len(resultIDs))
	for _, resultID := range resultIDs {
		res := c.resultsByID[resultID]
		if res == nil {
			continue
		}
		results = append(results, diagnosticResult{
			id:          resultID,
			res:         res,
			recordType:  res.recordType,
			description: res.description,
		})
	}
	c.egraphMu.RUnlock()

	var diagnostics cachemoneyExportDiagnostics
	for _, result := range results {
		payload := result.res.loadPayloadState()
		if !payload.hasValue || !payload.isObject || payload.self == nil {
			continue
		}
		typeName := cachemoneyExportDiagnosticTypeName(payload.self)
		field := cachemoneyExportDiagnosticField(result.res.loadResultCall())
		if _, ok := payload.self.(NonPersistedObject); ok {
			continue
		}
		encoder, ok := payload.self.(PersistedObject)
		if !ok {
			diagnostics.UnpersistableObjects = append(diagnostics.UnpersistableObjects, cachemoneyExportDiagnosticObject{
				ResultID:    result.id,
				TypeName:    typeName,
				Field:       field,
				RecordType:  result.recordType,
				Description: result.description,
			})
			continue
		}
		encodeCtx := ContextWithCachemoneyExport(context.WithoutCancel(ctx))
		if frame := result.res.loadResultCall(); frame != nil {
			encodeCtx = ContextWithCall(encodeCtx, frame)
		}
		encoding, err := encoder.EncodePersistedObject(encodeCtx, c)
		if err != nil {
			diagnostics.ObjectEncodeErrors = append(diagnostics.ObjectEncodeErrors, cachemoneyExportDiagnosticObject{
				ResultID:    result.id,
				TypeName:    typeName,
				Field:       field,
				RecordType:  result.recordType,
				Description: result.description,
				Error:       err.Error(),
			})
			continue
		}
		for _, link := range encoding.SnapshotLinks {
			if link.RefKey == "" || !c.cachemoneyExportSnapshotLinkIsMutable(ctx, link.RefKey) {
				continue
			}
			diagnostics.MutableSnapshotLinks = append(diagnostics.MutableSnapshotLinks, cachemoneyExportDiagnosticSnapshotLink{
				ResultID:    result.id,
				TypeName:    typeName,
				Field:       field,
				Role:        link.Role,
				RefKey:      link.RefKey,
				RecordType:  result.recordType,
				Description: result.description,
			})
		}
	}
	return diagnostics
}

func cachemoneyExportDiagnosticTypeName(self Typed) string {
	if self == nil || self.Type() == nil || self.Type().Name() == "" {
		return "<unknown>"
	}
	return self.Type().Name()
}

func cachemoneyExportDiagnosticField(frame *ResultCall) string {
	if frame == nil {
		return ""
	}
	if frame.Field != "" {
		return frame.Field
	}
	return frame.SyntheticOp
}

func (c *Cache) cachemoneyExportSnapshotLinkIsMutable(ctx context.Context, refKey string) bool {
	if c == nil || c.snapshotManager == nil || refKey == "" {
		return false
	}
	metadata, found, err := c.snapshotManager.SnapshotRecordMetadata(ctx, refKey)
	if err == nil && found {
		return metadata.Mutable
	}
	return false
}
