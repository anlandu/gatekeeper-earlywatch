// Package main implements a Kubernetes audit-webhook sink and Gatekeeper
// external-data provider for EarlyWatch ManualTouchCheck parity.
//
// It mirrors the behavior of EarlyWatch's audit-monitor for ManualTouchMonitor:
// the API server POSTs audit.k8s.io/v1 EventList batches to /audit, the sink
// evaluates completed CREATE/UPDATE/PATCH/DELETE audit events against
// ManualTouchMonitor CRs, and matching requests are recorded in a bounded
// in-memory cache. Gatekeeper's EWManualTouchCheck queries that cache through
// /validate-manual-touch. Optional ManualTouchEvent CR creation remains
// available for compatibility by enabling --record-events.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	defaultEventNamespace       = "early-watch-system"
	maxRequestBodyBytes         = 32 << 20 // 32 MiB
	namespaceCacheTTL           = 30 * time.Second
	defaultTouchCacheTTL        = 24 * time.Hour
	defaultTouchCacheMaxRecords = 10000
	manualTouchProviderPrefix   = "manualtouch"

	labelResource          = "earlywatch.io/resource"
	labelResourceNamespace = "earlywatch.io/resource-namespace"
	labelResourceName      = "earlywatch.io/resource-name"
	labelAPIGroup          = "earlywatch.io/api-group"
	labelOperation         = "earlywatch.io/operation"
)

var (
	monitorGVR    = schema.GroupVersionResource{Group: "earlywatch.io", Version: "v1alpha1", Resource: "manualtouchmonitors"}
	touchEventGVR = schema.GroupVersionResource{Group: "earlywatch.io", Version: "v1alpha1", Resource: "manualtouchevents"}
	namespaceGVR  = schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	touchEventGVK = schema.GroupVersionKind{Group: "earlywatch.io", Version: "v1alpha1", Kind: "ManualTouchEvent"}

	defaultUserAgentPatterns = []*regexp.Regexp{regexp.MustCompile(`^kubectl/`)}

	monitoredVerbs = map[string]string{
		"delete":           "DELETE",
		"deletecollection": "DELETE",
		"create":           "CREATE",
		"update":           "UPDATE",
		"patch":            "UPDATE",
	}
)

type patternCacheKey struct {
	name       string
	namespace  string
	generation int64
}

type patternCacheEntry struct {
	patterns []*regexp.Regexp
	valid    bool
}

type namespaceCacheEntry struct {
	labels    map[string]string
	expiresAt time.Time
}

type manualTouchMonitor struct {
	name                   string
	namespace              string
	generation             int64
	subjects               []monitorSubject
	operations             []string
	userAgentPatterns      []string
	excludeServiceAccounts []string
}

type monitorSubject struct {
	apiGroup          string
	resource          string
	namespaceSelector *metav1.LabelSelector
	selectorInvalid   bool
}

const stageResponseComplete = "ResponseComplete"

// AuditEventList is the audit.k8s.io/v1 EventList shape that Kubernetes' audit
// webhook backend POSTs to this service. Only fields needed for ManualTouch
// detection and event recording are modeled here.
type AuditEventList struct {
	Items []AuditEvent `json:"items"`
}

// AuditEvent is a minimal audit.k8s.io/v1 Event.
type AuditEvent struct {
	AuditID                  string           `json:"auditID"`
	Stage                    string           `json:"stage"`
	Verb                     string           `json:"verb"`
	User                     AuditUser        `json:"user"`
	UserAgent                string           `json:"userAgent"`
	SourceIPs                []string         `json:"sourceIPs"`
	ObjectRef                AuditObjectRef   `json:"objectRef"`
	RequestReceivedTimestamp metav1.MicroTime `json:"requestReceivedTimestamp"`
}

// AuditUser identifies the authenticated Kubernetes user from an audit event.
type AuditUser struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// AuditObjectRef identifies the Kubernetes object referenced by an audit event.
type AuditObjectRef struct {
	Resource   string `json:"resource"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	APIGroup   string `json:"apiGroup"`
	APIVersion string `json:"apiVersion"`
}

// --- Gatekeeper external-data wire types (v1beta1) ---

type providerRequest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Request    struct {
		Keys []string `json:"keys"`
	} `json:"request"`
}

type providerItem struct {
	Key   string `json:"key"`
	Value any    `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

type providerResponse struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Response   struct {
		Idempotent  bool           `json:"idempotent"`
		Items       []providerItem `json:"items"`
		SystemError string         `json:"systemError,omitempty"`
	} `json:"response"`
}

// TouchTarget is the admission/audit object identity used by the in-memory
// provider. Operation, API group, and resource are normalized before storage and
// lookups; namespace and name remain case-sensitive Kubernetes identifiers.
type TouchTarget struct {
	Operation string
	APIGroup  string
	Resource  string
	Namespace string
	Name      string
}

// TouchQuery is one decoded provider lookup.
type TouchQuery struct {
	Target TouchTarget
	Window time.Duration
}

// ManualTouchCache stores the latest observed touch time per target.
type ManualTouchCache struct {
	mu         sync.Mutex
	ttl        time.Duration
	maxRecords int
	records    map[TouchTarget]time.Time
}

func NewManualTouchCache(ttl time.Duration, maxRecords int) *ManualTouchCache {
	if ttl <= 0 {
		ttl = defaultTouchCacheTTL
	}
	if maxRecords <= 0 {
		maxRecords = defaultTouchCacheMaxRecords
	}
	return &ManualTouchCache{
		ttl:        ttl,
		maxRecords: maxRecords,
		records:    make(map[TouchTarget]time.Time),
	}
}

func (c *ManualTouchCache) Record(target TouchTarget, touchedAt time.Time) {
	if c == nil {
		return
	}
	if touchedAt.IsZero() {
		touchedAt = time.Now()
	}
	touchedAt = touchedAt.UTC()
	target = normalizeTouchTarget(target)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneExpiredLocked(touchedAt)
	if current, ok := c.records[target]; !ok || touchedAt.After(current) {
		c.records[target] = touchedAt
	}
	c.evictOverflowLocked()
}

func (c *ManualTouchCache) HasRecent(target TouchTarget, window time.Duration, now time.Time) bool {
	if c == nil || window <= 0 {
		return false
	}
	if now.IsZero() {
		now = time.Now()
	}
	now = now.UTC()
	target = normalizeTouchTarget(target)

	c.mu.Lock()
	defer c.mu.Unlock()

	c.pruneExpiredLocked(now)
	touchedAt, ok := c.records[target]
	if !ok {
		return false
	}
	if touchedAt.After(now) {
		return true
	}
	return !touchedAt.Before(now.Add(-window))
}

func (c *ManualTouchCache) pruneExpiredLocked(now time.Time) {
	if c.ttl <= 0 {
		return
	}
	cutoff := now.Add(-c.ttl)
	for target, touchedAt := range c.records {
		if touchedAt.Before(cutoff) {
			delete(c.records, target)
		}
	}
}

func (c *ManualTouchCache) evictOverflowLocked() {
	for len(c.records) > c.maxRecords {
		var oldestTarget TouchTarget
		var oldest time.Time
		first := true
		for target, touchedAt := range c.records {
			if first || touchedAt.Before(oldest) {
				oldestTarget = target
				oldest = touchedAt
				first = false
			}
		}
		delete(c.records, oldestTarget)
	}
}

func normalizeTouchTarget(target TouchTarget) TouchTarget {
	target.Operation = strings.ToUpper(target.Operation)
	target.APIGroup = strings.ToLower(target.APIGroup)
	target.Resource = strings.ToLower(target.Resource)
	return target
}

func touchTargetFromRecord(touch TouchRecord) TouchTarget {
	if touch.Event == nil {
		return TouchTarget{Operation: touch.Operation}
	}
	return TouchTarget{
		Operation: touch.Operation,
		APIGroup:  touch.Event.ObjectRef.APIGroup,
		Resource:  touch.Event.ObjectRef.Resource,
		Namespace: touch.Event.ObjectRef.Namespace,
		Name:      touch.Event.ObjectRef.Name,
	}
}

func manualTouchProviderKey(target TouchTarget, windowDuration string) string {
	target = normalizeTouchTarget(target)
	parts := []string{
		manualTouchProviderPrefix,
		encodeManualTouchKeyPart(target.Operation),
		encodeManualTouchKeyPart(target.APIGroup),
		encodeManualTouchKeyPart(target.Resource),
		encodeManualTouchKeyPart(target.Namespace),
		encodeManualTouchKeyPart(target.Name),
		encodeManualTouchKeyPart(windowDuration),
	}
	return strings.Join(parts, "|")
}

func parseManualTouchProviderKey(key string) (TouchQuery, error) {
	parts := strings.Split(key, "|")
	if (len(parts) != 7 && len(parts) != 8) || parts[0] != manualTouchProviderPrefix {
		return TouchQuery{}, fmt.Errorf("expected %s key with 6 encoded fields and optional request UID", manualTouchProviderPrefix)
	}

	decoded := make([]string, 6)
	for i := 1; i <= 6; i++ {
		value, err := decodeManualTouchKeyPart(parts[i])
		if err != nil {
			return TouchQuery{}, fmt.Errorf("decoding field %d: %w", i, err)
		}
		decoded[i-1] = value
	}

	// Field 7, when present, is a request UID cache-buster. It is not part of
	// the touch identity but must be valid base64 so malformed keys still surface
	// as item errors instead of silently bypassing validation.
	if len(parts) == 8 {
		if _, err := decodeManualTouchKeyPart(parts[7]); err != nil {
			return TouchQuery{}, fmt.Errorf("decoding field 7: %w", err)
		}
	}

	window, err := time.ParseDuration(decoded[5])
	if err != nil {
		return TouchQuery{}, fmt.Errorf("parsing windowDuration %q: %w", decoded[5], err)
	}
	if window <= 0 {
		return TouchQuery{}, fmt.Errorf("windowDuration must be positive")
	}

	return TouchQuery{
		Target: normalizeTouchTarget(TouchTarget{
			Operation: decoded[0],
			APIGroup:  decoded[1],
			Resource:  decoded[2],
			Namespace: decoded[3],
			Name:      decoded[4],
		}),
		Window: window,
	}, nil
}

func encodeManualTouchKeyPart(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}

func decodeManualTouchKeyPart(s string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

// TouchRecord carries the information needed to record one ManualTouchEvent.
type TouchRecord struct {
	Event            *AuditEvent
	Operation        string
	MonitorName      string
	MonitorNamespace string
}

// TouchDetector evaluates audit events against ManualTouchMonitor CRs.
type TouchDetector struct {
	Client dynamic.Interface

	nsCacheMu sync.RWMutex
	nsCache   map[string]namespaceCacheEntry

	patternCacheMu sync.RWMutex
	patternCache   map[patternCacheKey]patternCacheEntry
}

// Detect returns one TouchRecord for every ManualTouchMonitor that matches the
// supplied audit event. It intentionally mirrors upstream EarlyWatch behavior:
// only delete/deletecollection/create/update/patch verbs are recognized, PATCH
// maps to UPDATE, DELETECOLLECTION maps to DELETE, user-agent patterns default
// to ^kubectl/, excluded service-account usernames are exact matches, and each
// configured subject can match by API group/resource plus optional namespace
// selector.
func (d *TouchDetector) Detect(ctx context.Context, event *AuditEvent) ([]TouchRecord, error) {
	if d == nil || d.Client == nil || event == nil {
		return nil, nil
	}

	op, ok := monitoredVerbs[strings.ToLower(event.Verb)]
	if !ok {
		return nil, nil
	}

	monitorList, err := d.Client.Resource(monitorGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing ManualTouchMonitors: %w", err)
	}

	records := make([]TouchRecord, 0, len(monitorList.Items))
	for i := range monitorList.Items {
		monitor, err := parseManualTouchMonitor(&monitorList.Items[i])
		if err != nil {
			// CRD validation should prevent malformed monitors. If one exists, do
			// not let it block processing for other monitors in the batch.
			continue
		}

		if !d.monitorMatchesEvent(ctx, monitor, event, op) {
			continue
		}

		records = append(records, TouchRecord{
			Event:            event,
			Operation:        op,
			MonitorName:      monitor.name,
			MonitorNamespace: monitor.namespace,
		})
	}

	return records, nil
}

func (d *TouchDetector) monitorMatchesEvent(ctx context.Context, monitor manualTouchMonitor, event *AuditEvent, op string) bool {
	if !operationMatches(monitor.operations, op) {
		return false
	}

	patterns, ok := d.cachedPatterns(monitor)
	if !ok || !userAgentMatches(event.UserAgent, patterns) {
		return false
	}

	if isExcluded(event.User.Username, monitor.excludeServiceAccounts) {
		return false
	}

	for _, subj := range monitor.subjects {
		if d.subjectMatchesEvent(ctx, subj, event) {
			return true
		}
	}

	return false
}

func operationMatches(ops []string, op string) bool {
	for _, candidate := range ops {
		if candidate == op {
			return true
		}
	}
	return false
}

func (d *TouchDetector) cachedPatterns(monitor manualTouchMonitor) ([]*regexp.Regexp, bool) {
	if len(monitor.userAgentPatterns) == 0 {
		return defaultUserAgentPatterns, true
	}

	key := patternCacheKey{
		name:       monitor.name,
		namespace:  monitor.namespace,
		generation: monitor.generation,
	}

	d.patternCacheMu.RLock()
	if d.patternCache != nil {
		if entry, ok := d.patternCache[key]; ok {
			d.patternCacheMu.RUnlock()
			return entry.patterns, entry.valid
		}
	}
	d.patternCacheMu.RUnlock()

	d.patternCacheMu.Lock()
	defer d.patternCacheMu.Unlock()
	if d.patternCache != nil {
		if entry, ok := d.patternCache[key]; ok {
			return entry.patterns, entry.valid
		}
	} else {
		d.patternCache = make(map[patternCacheKey]patternCacheEntry)
	}

	compiled := make([]*regexp.Regexp, 0, len(monitor.userAgentPatterns))
	for _, pattern := range monitor.userAgentPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}

	entry := patternCacheEntry{patterns: compiled, valid: len(compiled) > 0}
	d.patternCache[key] = entry
	return entry.patterns, entry.valid
}

func userAgentMatches(userAgent string, patterns []*regexp.Regexp) bool {
	for _, re := range patterns {
		if re.MatchString(userAgent) {
			return true
		}
	}
	return false
}

func isExcluded(username string, exclusions []string) bool {
	for _, exclusion := range exclusions {
		if exclusion == username {
			return true
		}
	}
	return false
}

func (d *TouchDetector) subjectMatchesEvent(ctx context.Context, subj monitorSubject, event *AuditEvent) bool {
	if subj.apiGroup != event.ObjectRef.APIGroup {
		return false
	}

	if !strings.EqualFold(subj.resource, event.ObjectRef.Resource) {
		return false
	}

	if subj.selectorInvalid {
		return false
	}

	if subj.namespaceSelector != nil && !d.namespaceMatchesSelector(ctx, event.ObjectRef.Namespace, subj.namespaceSelector) {
		return false
	}

	return true
}

func (d *TouchDetector) namespaceMatchesSelector(ctx context.Context, namespace string, sel *metav1.LabelSelector) bool {
	if sel == nil || (len(sel.MatchLabels) == 0 && len(sel.MatchExpressions) == 0) {
		return true
	}

	selector, err := metav1.LabelSelectorAsSelector(sel)
	if err != nil {
		return false
	}

	nsLabels := d.namespaceLabels(ctx, namespace)
	if nsLabels == nil {
		return false
	}

	return selector.Matches(labels.Set(nsLabels))
}

func (d *TouchDetector) namespaceLabels(ctx context.Context, namespace string) map[string]string {
	now := time.Now()

	d.nsCacheMu.RLock()
	if d.nsCache != nil {
		if entry, ok := d.nsCache[namespace]; ok && now.Before(entry.expiresAt) {
			d.nsCacheMu.RUnlock()
			return entry.labels
		}
	}
	d.nsCacheMu.RUnlock()

	ns, err := d.Client.Resource(namespaceGVR).Get(ctx, namespace, metav1.GetOptions{})
	if err != nil {
		return nil
	}

	labelsCopy := map[string]string{}
	for k, v := range ns.GetLabels() {
		labelsCopy[k] = v
	}

	d.nsCacheMu.Lock()
	if d.nsCache == nil {
		d.nsCache = make(map[string]namespaceCacheEntry)
	}
	d.nsCache[namespace] = namespaceCacheEntry{labels: labelsCopy, expiresAt: now.Add(namespaceCacheTTL)}
	d.nsCacheMu.Unlock()

	return labelsCopy
}

func parseManualTouchMonitor(obj *unstructured.Unstructured) (manualTouchMonitor, error) {
	if obj == nil {
		return manualTouchMonitor{}, errors.New("nil monitor")
	}

	operations, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "operations")
	patterns, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "userAgentPatterns")
	exclusions, _, _ := unstructured.NestedStringSlice(obj.Object, "spec", "excludeServiceAccounts")

	subjectValues, _, _ := unstructured.NestedSlice(obj.Object, "spec", "subjects")
	subjects := make([]monitorSubject, 0, len(subjectValues))
	for _, value := range subjectValues {
		subjectMap, ok := value.(map[string]interface{})
		if !ok {
			continue
		}

		subj := monitorSubject{}
		if apiGroup, ok := subjectMap["apiGroup"].(string); ok {
			subj.apiGroup = apiGroup
		}
		if resource, ok := subjectMap["resource"].(string); ok {
			subj.resource = resource
		}

		if selectorMap, ok := subjectMap["namespaceSelector"].(map[string]interface{}); ok {
			selector := &metav1.LabelSelector{}
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, selector); err != nil {
				subj.selectorInvalid = true
			} else {
				subj.namespaceSelector = selector
			}
		}

		subjects = append(subjects, subj)
	}

	return manualTouchMonitor{
		name:                   obj.GetName(),
		namespace:              obj.GetNamespace(),
		generation:             obj.GetGeneration(),
		subjects:               subjects,
		operations:             operations,
		userAgentPatterns:      patterns,
		excludeServiceAccounts: exclusions,
	}, nil
}

// TouchRecorder creates EarlyWatch-compatible ManualTouchEvent CRs.
type TouchRecorder struct {
	Client         dynamic.Interface
	EventNamespace string
}

// Record creates a ManualTouchEvent using the same deterministic name, labels,
// and spec fields as upstream EarlyWatch's audit-monitor. AlreadyExists is
// ignored so retries of the same audit batch are idempotent.
func (r *TouchRecorder) Record(ctx context.Context, touch TouchRecord) error {
	if r == nil || r.Client == nil || touch.Event == nil {
		return nil
	}

	ns := r.EventNamespace
	if ns == "" {
		ns = defaultEventNamespace
	}

	event := touch.Event
	objectRef := event.ObjectRef
	name := sanitizeName("mte-" + event.AuditID + "-" + touch.MonitorNamespace + "-" + touch.MonitorName)

	mte := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "earlywatch.io/v1alpha1",
		"kind":       "ManualTouchEvent",
		"metadata": map[string]interface{}{
			"name":      name,
			"namespace": ns,
			"labels": map[string]interface{}{
				labelResource:          objectRef.Resource,
				labelResourceNamespace: objectRef.Namespace,
				labelResourceName:      objectRef.Name,
				labelAPIGroup:          objectRef.APIGroup,
				labelOperation:         touch.Operation,
			},
		},
		"spec": map[string]interface{}{
			"timestamp":         eventTimestamp(event),
			"user":              event.User.Username,
			"userAgent":         event.UserAgent,
			"operation":         touch.Operation,
			"apiGroup":          objectRef.APIGroup,
			"resource":          objectRef.Resource,
			"resourceName":      objectRef.Name,
			"resourceNamespace": objectRef.Namespace,
			"sourceIP":          firstSourceIP(event.SourceIPs),
			"auditID":           event.AuditID,
			"monitorName":       touch.MonitorName,
			"monitorNamespace":  touch.MonitorNamespace,
		},
	}}
	mte.SetGroupVersionKind(touchEventGVK)

	if _, err := r.Client.Resource(touchEventGVR).Namespace(ns).Create(ctx, mte, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return nil
		}
		return fmt.Errorf("creating ManualTouchEvent: %w", err)
	}

	return nil
}

func eventTime(event *AuditEvent) time.Time {
	if event == nil || event.RequestReceivedTimestamp.IsZero() {
		return time.Now().UTC()
	}
	return event.RequestReceivedTimestamp.Time.UTC()
}

func eventTimestamp(event *AuditEvent) string {
	return eventTime(event).Format(time.RFC3339Nano)
}

func firstSourceIP(ips []string) string {
	if len(ips) == 0 {
		return ""
	}
	return ips[0]
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 253 {
		result = result[:253]
	}
	return result
}

// AuditHandler receives audit.k8s.io/v1 EventList payloads at /audit.
type AuditHandler struct {
	Detector *TouchDetector
	Recorder *TouchRecorder
	Cache    *ManualTouchCache
	Logger   *slog.Logger
}

func (h *AuditHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	var eventList AuditEventList
	if err := json.NewDecoder(r.Body).Decode(&eventList); err != nil {
		logger.Warn("failed to decode audit EventList", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	for i := range eventList.Items {
		event := &eventList.Items[i]
		if event.Stage != stageResponseComplete {
			continue
		}

		if h.Detector == nil {
			continue
		}

		touches, err := h.Detector.Detect(r.Context(), event)
		if err != nil {
			logger.Warn("error detecting manual touch", "auditID", event.AuditID, "error", err)
			continue
		}

		for _, touch := range touches {
			if h.Cache != nil {
				h.Cache.Record(touchTargetFromRecord(touch), eventTime(touch.Event))
			}

			if h.Recorder != nil {
				if err := h.Recorder.Record(r.Context(), touch); err != nil {
					logger.Warn("error recording ManualTouchEvent", "auditID", event.AuditID, "monitor", touch.MonitorName, "error", err)
					continue
				}
				logger.Info("recorded ManualTouchEvent", "auditID", event.AuditID, "monitor", touch.MonitorName, "operation", touch.Operation)
				continue
			}

			logger.Info("recorded manual touch in cache", "auditID", event.AuditID, "monitor", touch.MonitorName, "operation", touch.Operation)
		}
	}

	w.WriteHeader(http.StatusOK)
}

// ManualTouchProviderHandler implements Gatekeeper's external-data provider
// endpoint for EWManualTouchCheck.
type ManualTouchProviderHandler struct {
	Cache  *ManualTouchCache
	Logger *slog.Logger
	Now    func() time.Time
}

func (h *ManualTouchProviderHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := h.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	defer r.Body.Close()

	var req providerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		logger.Warn("failed to decode ProviderRequest", "error", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	now := time.Now().UTC()
	if h.Now != nil {
		now = h.Now().UTC()
	}
	start := time.Now()

	logger.Info("external-data call received",
		"endpoint", "/validate-manual-touch",
		"remote", r.RemoteAddr,
		"keys", len(req.Request.Keys),
	)

	resp := providerResponse{
		APIVersion: "externaldata.gatekeeper.sh/v1beta1",
		Kind:       "ProviderResponse",
	}
	// The provider answers from mutable in-memory state and from a moving time
	// window, so responses should not be treated as idempotent/cacheable.
	resp.Response.Idempotent = false

	var touched, untouched, errored int
	for i, key := range req.Request.Keys {
		query, err := parseManualTouchProviderKey(key)
		if err != nil {
			errored++
			logger.Warn("rejected key",
				"idx", i,
				"error", err.Error(),
			)
			resp.Response.Items = append(resp.Response.Items, providerItem{Key: key, Error: err.Error()})
			continue
		}

		value := "untouched"
		if h.Cache != nil && h.Cache.HasRecent(query.Target, query.Window, now) {
			value = "touched"
			touched++
		} else {
			untouched++
		}
		logger.Info("evaluated key",
			"idx", i,
			"operation", query.Target.Operation,
			"apiGroup", query.Target.APIGroup,
			"resource", query.Target.Resource,
			"namespace", query.Target.Namespace,
			"name", query.Target.Name,
			"window", query.Window.String(),
			"result", value,
		)
		resp.Response.Items = append(resp.Response.Items, providerItem{Key: key, Value: value})
	}

	logger.Info("external-data call complete",
		"endpoint", "/validate-manual-touch",
		"keys", len(req.Request.Keys),
		"touched", touched,
		"untouched", untouched,
		"errored", errored,
		"duration_ms", time.Since(start).Milliseconds(),
	)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

type serverOptions struct {
	EventNamespace  string
	RecordEvents    bool
	CacheTTL        time.Duration
	CacheMaxRecords int
}

func newServer(client dynamic.Interface, opts serverOptions, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	cache := NewManualTouchCache(opts.CacheTTL, opts.CacheMaxRecords)
	handler := &AuditHandler{
		Detector: &TouchDetector{Client: client},
		Cache:    cache,
		Logger:   logger,
	}
	if opts.RecordEvents {
		handler.Recorder = &TouchRecorder{Client: client, EventNamespace: opts.EventNamespace}
	}
	mux.Handle("/audit", handler)
	mux.Handle("/validate-manual-touch", &ManualTouchProviderHandler{Cache: cache, Logger: logger})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func main() {
	var (
		listenAddr      string
		eventNamespace  string
		kubeconfig      string
		tlsCertFile     string
		tlsKeyFile      string
		recordEvents    bool
		touchCacheTTL   time.Duration
		touchCacheLimit int
	)

	flag.StringVar(&listenAddr, "listen-address", ":8443", "Address the audit monitor/provider HTTP server binds to.")
	flag.StringVar(&eventNamespace, "event-namespace", defaultEventNamespace, "Namespace where ManualTouchEvent resources are created when --record-events is enabled.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig. Defaults to $KUBECONFIG, then in-cluster config.")
	flag.StringVar(&tlsCertFile, "tls-cert-file", "", "TLS serving certificate. If set with --tls-private-key-file, serves HTTPS.")
	flag.StringVar(&tlsKeyFile, "tls-private-key-file", "", "TLS private key. If set with --tls-cert-file, serves HTTPS.")
	flag.BoolVar(&recordEvents, "record-events", false, "Also create ManualTouchEvent CRs for compatibility. The provider cache is always populated.")
	flag.DurationVar(&touchCacheTTL, "touch-cache-ttl", defaultTouchCacheTTL, "How long manual touch records remain in the in-memory provider cache.")
	flag.IntVar(&touchCacheLimit, "touch-cache-max-records", defaultTouchCacheMaxRecords, "Maximum number of manual touch targets retained in the in-memory provider cache.")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := buildKubeConfig(kubeconfig)
	if err != nil {
		logger.Error("unable to load Kubernetes config", "error", err)
		os.Exit(1)
	}

	client, err := dynamic.NewForConfig(cfg)
	if err != nil {
		logger.Error("unable to build dynamic client", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr: listenAddr,
		Handler: newServer(client, serverOptions{
			EventNamespace:  eventNamespace,
			RecordEvents:    recordEvents,
			CacheTTL:        touchCacheTTL,
			CacheMaxRecords: touchCacheLimit,
		}, logger),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		if tlsCertFile != "" || tlsKeyFile != "" {
			if tlsCertFile == "" || tlsKeyFile == "" {
				serverErr <- errors.New("both --tls-cert-file and --tls-private-key-file are required for HTTPS")
				return
			}
			logger.Info("starting ManualTouch audit monitor/provider", "address", listenAddr, "tls", true, "recordEvents", recordEvents)
			serverErr <- srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
			return
		}

		logger.Info("starting ManualTouch audit monitor/provider", "address", listenAddr, "tls", false, "recordEvents", recordEvents)
		serverErr <- srv.ListenAndServe()
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server exited", "error", err)
			os.Exit(1)
		}
	case sig := <-quit:
		logger.Info("received shutdown signal", "signal", sig.String())
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}

func buildKubeConfig(kubeconfig string) (*rest.Config, error) {
	if kubeconfig == "" {
		kubeconfig = os.Getenv("KUBECONFIG")
	}
	if kubeconfig != "" {
		return clientcmd.BuildConfigFromFlags("", kubeconfig)
	}
	return rest.InClusterConfig()
}
