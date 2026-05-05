package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestDefaultKubectlUserAgentCreatesManualTouchEvent(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)

	event := auditEvent("Audit.ID", "update", stageResponseComplete, "alice", "kubectl/v1.29.0 (linux/amd64)", "team-a")
	postAudit(t, client, event)

	events := listTouchEvents(t, client)
	if len(events.Items) != 1 {
		t.Fatalf("got %d ManualTouchEvents, want 1", len(events.Items))
	}

	mte := events.Items[0]
	if got, want := mte.GetName(), "mte-audit-id-early-watch-system-deployments"; got != want {
		t.Fatalf("event name = %q, want %q", got, want)
	}
	if got, want := mte.GetLabels()[labelResource], "deployments"; got != want {
		t.Fatalf("resource label = %q, want %q", got, want)
	}
	if got, want := mte.GetLabels()[labelResourceNamespace], "team-a"; got != want {
		t.Fatalf("namespace label = %q, want %q", got, want)
	}
	if got, want := mte.GetLabels()[labelResourceName], "web"; got != want {
		t.Fatalf("name label = %q, want %q", got, want)
	}
	if got, want := mte.GetLabels()[labelAPIGroup], "apps"; got != want {
		t.Fatalf("apiGroup label = %q, want %q", got, want)
	}
	if got, want := mte.GetLabels()[labelOperation], "UPDATE"; got != want {
		t.Fatalf("operation label = %q, want %q", got, want)
	}

	assertNestedString(t, mte.Object, "alice", "spec", "user")
	assertNestedString(t, mte.Object, "UPDATE", "spec", "operation")
	assertNestedString(t, mte.Object, "apps", "spec", "apiGroup")
	assertNestedString(t, mte.Object, "deployments", "spec", "resource")
	assertNestedString(t, mte.Object, "web", "spec", "resourceName")
	assertNestedString(t, mte.Object, "team-a", "spec", "resourceNamespace")
	assertNestedString(t, mte.Object, "Audit.ID", "spec", "auditID")
	assertNestedString(t, mte.Object, "deployments", "spec", "monitorName")
	assertNestedString(t, mte.Object, "early-watch-system", "spec", "monitorNamespace")
}

func TestNonResponseCompleteIgnored(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)

	postAudit(t, client, auditEvent("audit-1", "update", "RequestReceived", "alice", "kubectl/v1.29.0", "team-a"))

	if events := listTouchEvents(t, client); len(events.Items) != 0 {
		t.Fatalf("got %d ManualTouchEvents, want 0", len(events.Items))
	}
}

func TestExcludedServiceAccountIgnored(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", map[string]interface{}{
			"excludeServiceAccounts": []interface{}{"system:serviceaccount:ci:bot"},
		}),
	)

	postAudit(t, client, auditEvent("audit-1", "update", stageResponseComplete, "system:serviceaccount:ci:bot", "kubectl/v1.29.0", "team-a"))

	if events := listTouchEvents(t, client); len(events.Items) != 0 {
		t.Fatalf("got %d ManualTouchEvents, want 0", len(events.Items))
	}
}

func TestNamespaceSelectorMatchAndMismatch(t *testing.T) {
	selector := map[string]interface{}{
		"matchLabels": map[string]interface{}{"env": "prod"},
	}

	t.Run("match", func(t *testing.T) {
		client := fakeClient(
			namespace("prod-ns", map[string]string{"env": "prod"}),
			deploymentUpdateMonitor("deployments", "early-watch-system", map[string]interface{}{"namespaceSelector": selector}),
		)

		postAudit(t, client, auditEvent("audit-1", "update", stageResponseComplete, "alice", "kubectl/v1.29.0", "prod-ns"))

		if events := listTouchEvents(t, client); len(events.Items) != 1 {
			t.Fatalf("got %d ManualTouchEvents, want 1", len(events.Items))
		}
	})

	t.Run("mismatch", func(t *testing.T) {
		client := fakeClient(
			namespace("dev-ns", map[string]string{"env": "dev"}),
			deploymentUpdateMonitor("deployments", "early-watch-system", map[string]interface{}{"namespaceSelector": selector}),
		)

		postAudit(t, client, auditEvent("audit-1", "update", stageResponseComplete, "alice", "kubectl/v1.29.0", "dev-ns"))

		if events := listTouchEvents(t, client); len(events.Items) != 0 {
			t.Fatalf("got %d ManualTouchEvents, want 0", len(events.Items))
		}
	})
}

func TestPatchMapsToUpdate(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)

	postAudit(t, client, auditEvent("audit-1", "patch", stageResponseComplete, "alice", "kubectl/v1.29.0", "team-a"))

	events := listTouchEvents(t, client)
	if len(events.Items) != 1 {
		t.Fatalf("got %d ManualTouchEvents, want 1", len(events.Items))
	}
	assertNestedString(t, events.Items[0].Object, "UPDATE", "spec", "operation")
}

func TestDuplicateDeliveryIsIdempotent(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)

	event := auditEvent("audit-1", "update", stageResponseComplete, "alice", "kubectl/v1.29.0", "team-a")
	postAudit(t, client, event)
	postAudit(t, client, event)

	if events := listTouchEvents(t, client); len(events.Items) != 1 {
		t.Fatalf("got %d ManualTouchEvents after duplicate delivery, want 1", len(events.Items))
	}
}

func TestAuditPopulatesCacheProviderReturnsTouched(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)
	cache := NewManualTouchCache(24*time.Hour, 100)
	event := auditEvent("audit-1", "update", stageResponseComplete, "alice", "kubectl/v1.29.0", "team-a")

	postAuditWithoutRecorder(t, client, cache, event)

	key := manualTouchProviderKey(TouchTarget{
		Operation: "update",
		APIGroup:  "Apps",
		Resource:  "Deployments",
		Namespace: "team-a",
		Name:      "web",
	}, "1h")
	resp := postProvider(t, cache, eventTime(&event).Add(30*time.Minute), key)

	if got, want := len(resp.Response.Items), 1; got != want {
		t.Fatalf("provider items = %d, want %d", got, want)
	}
	if item := resp.Response.Items[0]; item.Error != "" || item.Value != "touched" {
		t.Fatalf("provider item = %#v, want touched with no error", item)
	}
}

func TestManualTouchProviderReturnsUntouchedForDifferentNameOrWindow(t *testing.T) {
	cache := NewManualTouchCache(24*time.Hour, 100)
	touchedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	cache.Record(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "web",
	}, touchedAt)

	differentNameKey := manualTouchProviderKey(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "api",
	}, "1h")
	expiredWindowKey := manualTouchProviderKey(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "web",
	}, "30m")

	resp := postProvider(t, cache, touchedAt.Add(2*time.Hour), differentNameKey, expiredWindowKey)
	if got, want := len(resp.Response.Items), 2; got != want {
		t.Fatalf("provider items = %d, want %d", got, want)
	}
	for _, item := range resp.Response.Items {
		if item.Error != "" || item.Value != "untouched" {
			t.Fatalf("provider item = %#v, want untouched with no error", item)
		}
	}
}

func TestManualTouchProviderKeyAcceptsRequestUIDCacheBuster(t *testing.T) {
	cache := NewManualTouchCache(24*time.Hour, 100)
	touchedAt := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	cache.Record(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "web",
	}, touchedAt)

	key := manualTouchProviderKey(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "web",
	}, "1h") + "|" + encodeManualTouchKeyPart("admission-review-uid")

	resp := postProvider(t, cache, touchedAt.Add(time.Minute), key)
	if item := resp.Response.Items[0]; item.Error != "" || item.Value != "touched" {
		t.Fatalf("provider item = %#v, want touched with no error", item)
	}
}

func TestManualTouchProviderMalformedKeyReturnsItemError(t *testing.T) {
	resp := postProvider(t, NewManualTouchCache(24*time.Hour, 100), time.Now(), "not-a-manual-touch-key")

	if got, want := len(resp.Response.Items), 1; got != want {
		t.Fatalf("provider items = %d, want %d", got, want)
	}
	if item := resp.Response.Items[0]; item.Error == "" {
		t.Fatalf("provider item = %#v, want item error", item)
	}
}

func TestRecorderDisabledCreatesNoCRsWhileCacheRecords(t *testing.T) {
	client := fakeClient(
		namespace("team-a", map[string]string{"env": "prod"}),
		deploymentUpdateMonitor("deployments", "early-watch-system", nil),
	)
	cache := NewManualTouchCache(24*time.Hour, 100)
	event := auditEvent("audit-1", "update", stageResponseComplete, "alice", "kubectl/v1.29.0", "team-a")

	postAuditWithoutRecorder(t, client, cache, event)

	if events := listTouchEvents(t, client); len(events.Items) != 0 {
		t.Fatalf("got %d ManualTouchEvents, want 0", len(events.Items))
	}

	key := manualTouchProviderKey(TouchTarget{
		Operation: "UPDATE",
		APIGroup:  "apps",
		Resource:  "deployments",
		Namespace: "team-a",
		Name:      "web",
	}, "1h")
	resp := postProvider(t, cache, eventTime(&event).Add(time.Minute), key)
	if item := resp.Response.Items[0]; item.Error != "" || item.Value != "touched" {
		t.Fatalf("provider item = %#v, want touched with no error", item)
	}
}

func fakeClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	listKinds := map[schema.GroupVersionResource]string{
		monitorGVR:    "ManualTouchMonitorList",
		touchEventGVR: "ManualTouchEventList",
		namespaceGVR:  "NamespaceList",
	}
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
}

func postAudit(t *testing.T, client *dynamicfake.FakeDynamicClient, events ...AuditEvent) {
	t.Helper()
	postAuditWithRecorder(t, client, NewManualTouchCache(defaultTouchCacheTTL, defaultTouchCacheMaxRecords), true, events...)
}

func postAuditWithoutRecorder(t *testing.T, client *dynamicfake.FakeDynamicClient, cache *ManualTouchCache, events ...AuditEvent) {
	t.Helper()
	postAuditWithRecorder(t, client, cache, false, events...)
}

func postAuditWithRecorder(t *testing.T, client *dynamicfake.FakeDynamicClient, cache *ManualTouchCache, recordEvents bool, events ...AuditEvent) {
	t.Helper()

	body, err := json.Marshal(AuditEventList{Items: events})
	if err != nil {
		t.Fatalf("marshal audit events: %v", err)
	}

	handler := &AuditHandler{
		Detector: &TouchDetector{Client: client},
		Cache:    cache,
	}
	if recordEvents {
		handler.Recorder = &TouchRecorder{Client: client, EventNamespace: defaultEventNamespace}
	}

	req := httptest.NewRequest(http.MethodPost, "/audit", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("audit handler status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func postProvider(t *testing.T, cache *ManualTouchCache, now time.Time, keys ...string) providerResponse {
	t.Helper()

	reqBody := providerRequest{
		APIVersion: "externaldata.gatekeeper.sh/v1beta1",
		Kind:       "ProviderRequest",
	}
	reqBody.Request.Keys = keys

	body, err := json.Marshal(reqBody)
	if err != nil {
		t.Fatalf("marshal provider request: %v", err)
	}

	handler := &ManualTouchProviderHandler{Cache: cache}
	if !now.IsZero() {
		handler.Now = func() time.Time { return now }
	}

	req := httptest.NewRequest(http.MethodPost, "/validate-manual-touch", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("provider handler status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var resp providerResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal provider response: %v", err)
	}
	return resp
}

func listTouchEvents(t *testing.T, client *dynamicfake.FakeDynamicClient) *unstructured.UnstructuredList {
	t.Helper()
	events, err := client.Resource(touchEventGVR).Namespace(defaultEventNamespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list ManualTouchEvents: %v", err)
	}
	return events
}

func namespace(name string, labels map[string]string) *unstructured.Unstructured {
	labelMap := make(map[string]interface{}, len(labels))
	for key, value := range labels {
		labelMap[key] = value
	}
	ns := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "v1",
		"kind":       "Namespace",
		"metadata": map[string]interface{}{
			"name":   name,
			"labels": labelMap,
		},
	}}
	ns.SetGroupVersionKind(schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Namespace"})
	return ns
}

func deploymentUpdateMonitor(name, ns string, overrides map[string]interface{}) *unstructured.Unstructured {
	subject := map[string]interface{}{
		"apiGroup": "apps",
		"resource": "deployments",
	}
	if selector, ok := overrides["namespaceSelector"]; ok {
		subject["namespaceSelector"] = selector
	}

	spec := map[string]interface{}{
		"subjects":   []interface{}{subject},
		"operations": []interface{}{"UPDATE"},
	}
	for key, value := range overrides {
		if key == "namespaceSelector" {
			continue
		}
		spec[key] = value
	}

	monitor := &unstructured.Unstructured{Object: map[string]interface{}{
		"apiVersion": "earlywatch.io/v1alpha1",
		"kind":       "ManualTouchMonitor",
		"metadata": map[string]interface{}{
			"name":       name,
			"namespace":  ns,
			"generation": int64(1),
		},
		"spec": spec,
	}}
	monitor.SetGroupVersionKind(schema.GroupVersionKind{Group: "earlywatch.io", Version: "v1alpha1", Kind: "ManualTouchMonitor"})
	return monitor
}

func auditEvent(auditID, verb, stage, username, userAgent, namespace string) AuditEvent {
	return AuditEvent{
		AuditID:   auditID,
		Stage:     stage,
		Verb:      verb,
		User:      AuditUser{Username: username},
		UserAgent: userAgent,
		SourceIPs: []string{"203.0.113.10"},
		ObjectRef: AuditObjectRef{
			APIGroup:   "apps",
			APIVersion: "v1",
			Resource:   "deployments",
			Namespace:  namespace,
			Name:       "web",
		},
		RequestReceivedTimestamp: metav1.MicroTime{Time: time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)},
	}
}

func assertNestedString(t *testing.T, obj map[string]interface{}, want string, fields ...string) {
	t.Helper()
	got, found, err := unstructured.NestedString(obj, fields...)
	if err != nil {
		t.Fatalf("nested %v error: %v", fields, err)
	}
	if !found {
		t.Fatalf("nested %v missing", fields)
	}
	if got != want {
		t.Fatalf("nested %v = %q, want %q", fields, got, want)
	}
}
