// Package main implements a Kubernetes audit-webhook sink that produces
// EarlyWatch-compatible ManualTouchEvent custom resources.
//
// It mirrors the behavior of EarlyWatch's audit-monitor for ManualTouchMonitor:
// the API server POSTs audit.k8s.io/v1 EventList batches to /audit, the sink
// evaluates completed CREATE/UPDATE/PATCH/DELETE audit events against
// ManualTouchMonitor CRs, and matching requests are recorded as
// ManualTouchEvent CRs for Gatekeeper's EWManualTouchCheck to consume.
package main

import (
	"context"
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
	defaultEventNamespace = "early-watch-system"
	maxRequestBodyBytes   = 32 << 20 // 32 MiB
	namespaceCacheTTL     = 30 * time.Second

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

func eventTimestamp(event *AuditEvent) string {
	if event.RequestReceivedTimestamp.IsZero() {
		return time.Now().UTC().Format(time.RFC3339Nano)
	}
	return event.RequestReceivedTimestamp.Time.UTC().Format(time.RFC3339Nano)
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

		touches, err := h.Detector.Detect(r.Context(), event)
		if err != nil {
			logger.Warn("error detecting manual touch", "auditID", event.AuditID, "error", err)
			continue
		}

		for _, touch := range touches {
			if err := h.Recorder.Record(r.Context(), touch); err != nil {
				logger.Warn("error recording ManualTouchEvent", "auditID", event.AuditID, "monitor", touch.MonitorName, "error", err)
				continue
			}
			logger.Info("recorded ManualTouchEvent", "auditID", event.AuditID, "monitor", touch.MonitorName, "operation", touch.Operation)
		}
	}

	w.WriteHeader(http.StatusOK)
}

func newServer(client dynamic.Interface, eventNamespace string, logger *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	handler := &AuditHandler{
		Detector: &TouchDetector{Client: client},
		Recorder: &TouchRecorder{Client: client, EventNamespace: eventNamespace},
		Logger:   logger,
	}
	mux.Handle("/audit", handler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	return mux
}

func main() {
	var (
		listenAddr     string
		eventNamespace string
		kubeconfig     string
		tlsCertFile    string
		tlsKeyFile     string
	)

	flag.StringVar(&listenAddr, "listen-address", ":8090", "Address the audit monitor HTTP server binds to.")
	flag.StringVar(&eventNamespace, "event-namespace", defaultEventNamespace, "Namespace where ManualTouchEvent resources are created.")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig. Defaults to $KUBECONFIG, then in-cluster config.")
	flag.StringVar(&tlsCertFile, "tls-cert-file", "", "TLS serving certificate. If set with --tls-private-key-file, serves HTTPS.")
	flag.StringVar(&tlsKeyFile, "tls-private-key-file", "", "TLS private key. If set with --tls-cert-file, serves HTTPS.")
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
		Addr:              listenAddr,
		Handler:           newServer(client, eventNamespace, logger),
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
			logger.Info("starting ManualTouch audit monitor", "address", listenAddr, "tls", true)
			serverErr <- srv.ListenAndServeTLS(tlsCertFile, tlsKeyFile)
			return
		}

		logger.Info("starting ManualTouch audit monitor", "address", listenAddr, "tls", false)
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
