package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fatalf("usage: %s <command> [args...]", filepath.Base(os.Args[0]))
	}

	var err error
	switch os.Args[1] {
	case "write-parity-coverage-artifact":
		err = expectArgs(1, os.Args[2:], "<artifact>")
		if err == nil {
			err = writeParityCoverageArtifact(os.Args[2])
		}
	case "record-upstream-earlywatch-catalog":
		err = expectArgs(4, os.Args[2:], "<upstream-dir> <artifact> <repo-url> <expected-commit>")
		if err == nil {
			err = recordUpstreamEarlyWatchCatalog(os.Args[2], os.Args[3], os.Args[4], os.Args[5])
		}
	case "verify-gatekeeper-release":
		err = expectArgs(3, os.Args[2:], "<release-json> <summary-json> <expected-chart-version>")
		if err == nil {
			err = verifyGatekeeperRelease(os.Args[2], os.Args[3], os.Args[4])
		}
	case "gatekeeper-args-patch":
		err = expectArgs(2, os.Args[2:], "<deployment-json> <patch-json>")
		if err == nil {
			err = writeGatekeeperArgsPatch(os.Args[2], os.Args[3])
		}
	case "gatekeeper-vwc-patch":
		err = expectArgs(2, os.Args[2:], "<validating-webhook-configuration-json> <patch-json>")
		if err == nil {
			err = writeGatekeeperValidatingWebhookPatch(os.Args[2], os.Args[3])
		}
	case "approval-constraint-patch":
		err = expectArgs(2, os.Args[2:], "<public-key-pem> <patch-json>")
		if err == nil {
			err = writeApprovalConstraintPatch(os.Args[2], os.Args[3])
		}
	case "manual-touch-audit-event":
		err = expectArgs(2, os.Args[2:], "<payload-json> <user-agent>")
		if err == nil {
			err = writeManualTouchAuditEvent(os.Args[2], os.Args[3])
		}
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}

	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

func expectArgs(want int, got []string, usage string) error {
	if len(got) != want {
		return fmt.Errorf("usage: %s %s", filepath.Base(os.Args[0]), usage)
	}
	return nil
}

func writeJSONFile(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func writeParityCoverageArtifact(artifactPath string) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}

	suitePath := filepath.Join(root, "tests", "suite.yaml")
	parsed, err := parseGatorSuite(suitePath)
	if err != nil {
		return err
	}

	requiredSuiteCases := map[string][]string{
		"existing-resources": {
			"deny-delete-when-pod-matches",
			"allow-delete-when-no-pod-matches",
		},
		"name-reference-check": {
			"deny-delete-when-deployment-references",
			"allow-delete-when-no-reference",
		},
		"name-reference-check-default-version": {
			"deny-delete-when-resource-version-omitted-defaults-to-v1",
		},
		"annotation-check": {
			"deny-when-annotation-missing",
			"deny-when-annotation-wrong",
			"allow-when-annotation-correct",
		},
		"checklock": {
			"deny-delete-when-locked",
			"deny-delete-when-lock-value-is-not-true",
			"deny-update-when-locked",
			"allow-unlock-only-update",
			"allow-lock-annotation-value-only-update",
			"allow-unlock-only-update-with-server-managed-metadata",
			"deny-unlock-plus-other-change",
			"deny-unlock-plus-status-change",
			"allow-delete-when-unlocked",
			"deny-scale-subresource-when-parent-deployment-locked",
			"allow-scale-subresource-when-parent-deployment-unlocked",
		},
		"expression-check": {
			"deny-delete-in-production",
			"allow-delete-elsewhere",
		},
		"expression-check-regex": {
			"deny-by-namespace-regex",
			"deny-by-name-regex",
			"allow-when-no-predicate-matches",
		},
		"service-pod-selector-check": {
			"allow-create-with-zero-matching-pods",
			"allow-create-with-matching-pod",
			"deny-update-when-old-had-pod-new-has-zero",
			"allow-update-when-old-selector-had-zero-pods",
			"allow-update-when-new-selector-has-pod",
			"allow-headless-service-update",
			"allow-when-new-selector-is-empty-map-and-pods-exist",
			"deny-when-old-empty-selector-matched-pods-and-new-selector-matches-none",
			"deny-when-non-headless-service-changes-to-headless-and-new-selector-matches-none",
		},
		"data-key-safety-check": {
			"deny-when-key-still-referenced",
			"allow-when-only-other-keys-referenced",
			"deny-when-binary-data-key-still-referenced",
		},
		"data-key-safety-check-default-version": {
			"deny-when-resource-version-omitted-defaults-to-v1",
		},
		"existing-resources-static-selector": {
			"deny-when-In-matches",
			"allow-when-In-does-not-match",
		},
		"existing-resources-upstream-parity": {
			"deny-no-selector-matches-any-dependent",
		},
		"existing-resources-empty-static-selector": {
			"deny-empty-static-selector-matches-any-dependent",
		},
		"existing-resources-field-selector-precedence": {
			"allow-when-field-selector-does-not-match-even-if-static-would",
		},
		"existing-resources-namespace-delete-scope": {
			"deny-namespace-delete-when-dependent-exists-inside-namespace",
		},
		"malformed-inputs": {
			"missing-annotations-map",
		},
	}

	requiredGoTests := map[string][]string{
		"provider/main_test.go": {
			"TestDeleteValid",
			"TestDeleteTampered",
			"TestPKCS1PublicKeyRejectedForUpstreamParity",
			"TestUpdateValid",
			"TestUpdateUnsignedSpecChange",
			"TestUpdateNormalizationIgnoresStatusAndManagedMetadata",
			"TestUpdateNormalizationStripsChangeAnnotationAndDropsEmptyAnnotations",
			"TestUpdateNormalizationDropsNullAnnotations",
			"TestUpdateBase64KeyAllowsPipeInJSON",
			"TestUpdateTamperedMeaningfulChangeFails",
			"TestMergePatchRemoval",
		},
		"touch-monitor/main_test.go": {
			"TestDefaultKubectlUserAgentCreatesManualTouchEvent",
			"TestNonResponseCompleteIgnored",
			"TestExcludedServiceAccountIgnored",
			"TestNamespaceSelectorMatchAndMismatch",
			"TestPatchMapsToUpdate",
			"TestDuplicateDeliveryIsIdempotent",
			"TestAuditPopulatesCacheProviderReturnsTouched",
			"TestManualTouchProviderReturnsUntouchedForDifferentNameOrWindow",
			"TestManualTouchProviderKeyAcceptsRequestUIDCacheBuster",
			"TestManualTouchProviderMalformedKeyReturnsItemError",
			"TestRecorderDisabledCreatesNoCRsWhileCacheRecords",
		},
	}

	liveCapabilities := []string{
		"ExistingResourcesCheck",
		"NameReferenceCheck",
		"AnnotationCheck",
		"ApprovalCheck",
		"CheckLock",
		"ExpressionCheck",
		"ManualTouchCheck",
		"ServicePodSelectorCheck",
		"DataKeySafetyCheck",
	}

	missing := make([]string, 0)
	for _, testName := range sortedKeys(requiredSuiteCases) {
		cases := requiredSuiteCases[testName]
		parsedCases, ok := parsed[testName]
		if !ok {
			missing = append(missing, fmt.Sprintf("suite missing test %s", testName))
			continue
		}
		have := make(map[string]struct{}, len(parsedCases))
		for _, c := range parsedCases {
			have[c] = struct{}{}
		}
		for _, c := range cases {
			if _, ok := have[c]; !ok {
				missing = append(missing, fmt.Sprintf("suite %s missing case %s", testName, c))
			}
		}
	}

	for _, rel := range sortedKeys(requiredGoTests) {
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return err
		}
		for _, test := range requiredGoTests[rel] {
			if !strings.Contains(string(content), fmt.Sprintf("func %s(", test)) {
				missing = append(missing, fmt.Sprintf("%s missing %s", rel, test))
			}
		}
	}

	suiteCaseCount := 0
	for _, cases := range parsed {
		suiteCaseCount += len(cases)
	}

	summary := map[string]any{
		"scope":                  "functional EarlyWatch capability parity implemented with Gatekeeper, not EarlyWatch API compatibility",
		"static_gator_suite":     suitePath,
		"required_suite_cases":   requiredSuiteCases,
		"required_go_tests":      requiredGoTests,
		"live_kind_capabilities": liveCapabilities,
		"suite_test_count":       len(parsed),
		"suite_case_count":       suiteCaseCount,
		"missing":                missing,
	}
	if err := writeJSONFile(artifactPath, summary); err != nil {
		return err
	}
	if len(missing) > 0 {
		for _, item := range missing {
			fmt.Fprintln(os.Stderr, item)
		}
		return errors.New("parity coverage catalog verification failed")
	}
	fmt.Printf("Parity coverage catalog verified: %d gator tests, %d gator cases, %d live capabilities\n", len(parsed), suiteCaseCount, len(liveCapabilities))
	return nil
}

func parseGatorSuite(path string) (map[string][]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	testName := regexp.MustCompile(`^  - name: (.+)$`)
	caseName := regexp.MustCompile(`^      - name: (.+)$`)
	parsed := map[string][]string{}
	current := ""

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if matches := testName.FindStringSubmatch(line); matches != nil {
			current = strings.TrimSpace(matches[1])
			parsed[current] = []string{}
			continue
		}
		if matches := caseName.FindStringSubmatch(line); matches != nil && current != "" {
			parsed[current] = append(parsed[current], strings.TrimSpace(matches[1]))
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return parsed, nil
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func recordUpstreamEarlyWatchCatalog(upstreamDir, artifactPath, repoURL, expectedCommit string) error {
	cmd := exec.Command("git", "-C", upstreamDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("git rev-parse failed: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return err
	}
	actualCommit := strings.TrimSpace(string(out))

	e2e := []string{}
	e2ePath := filepath.Join(upstreamDir, "test", "e2e", "e2e_test.go")
	if content, err := os.ReadFile(e2ePath); err == nil {
		re := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]+)\(`)
		for _, match := range re.FindAllSubmatch(content, -1) {
			e2e = append(e2e, string(match[1]))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	installManifests := append([]string{}, relglob(upstreamDir, "config/webhook/*.yaml")...)
	installManifests = append(installManifests, relglob(upstreamDir, "config/rbac/*.yaml")...)
	installManifests = append(installManifests, relglob(upstreamDir, "config/audit/*.yaml")...)
	installManifests = append(installManifests, relglob(upstreamDir, "pkg/install/manifests/*.yaml")...)
	installManifests = append(installManifests, relglob(upstreamDir, "pkg/install/manifests/manual-touch/*.yaml")...)
	sort.Strings(installManifests)

	catalog := map[string]any{
		"repo_url":                 repoURL,
		"expected_commit":          expectedCommit,
		"actual_commit":            actualCommit,
		"deployed_by":              "watchctl install --manual-touch with locally built images",
		"functional_parity_target": "Gatekeeper implementation; upstream EarlyWatch CRD/API compatibility is intentionally out of scope",
		"rule_type_examples":       relglob(upstreamDir, "docs/examples/*.yaml"),
		"sample_validators":        relglob(upstreamDir, "config/samples/*.yaml"),
		"demo_scripts":             relglob(upstreamDir, "scripts/demo-*.sh"),
		"install_manifests":        installManifests,
		"e2e_tests":                e2e,
	}
	if err := writeJSONFile(artifactPath, catalog); err != nil {
		return err
	}
	if actualCommit != expectedCommit {
		return fmt.Errorf("upstream EarlyWatch commit mismatch: got %s, expected %s", actualCommit, expectedCommit)
	}
	return nil
}

func relglob(root, pattern string) []string {
	matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
	if err != nil {
		return []string{}
	}
	results := make([]string, 0, len(matches))
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() {
			continue
		}
		rel, err := filepath.Rel(root, match)
		if err != nil {
			continue
		}
		results = append(results, filepath.ToSlash(rel))
	}
	sort.Strings(results)
	return results
}

func verifyGatekeeperRelease(releasePath, summaryPath, expected string) error {
	var releases []map[string]any
	if err := readJSONFile(releasePath, &releases); err != nil {
		return err
	}
	if len(releases) == 0 {
		return errors.New("Gatekeeper Helm release was not found")
	}

	release := releases[0]
	chart := stringValue(release["chart"])
	appVersion := stringValue(release["app_version"])
	if appVersion == "" {
		appVersion = stringValue(release["appVersion"])
	}
	expectedChart := "gatekeeper-" + expected

	summary := map[string]any{
		"expected_chart_version": expected,
		"actual_chart":           chart,
		"actual_app_version":     appVersion,
	}
	if err := writeJSONFile(summaryPath, summary); err != nil {
		return err
	}
	if chart != expectedChart {
		return fmt.Errorf("Gatekeeper chart mismatch: got %q, expected %q", chart, expectedChart)
	}
	if !strings.HasPrefix(appVersion, "v3.22") {
		return fmt.Errorf("Gatekeeper appVersion %q is not v3.22.x", appVersion)
	}
	return nil
}

func writeGatekeeperArgsPatch(deployPath, patchPath string) error {
	var obj map[string]any
	if err := readJSONFile(deployPath, &obj); err != nil {
		return err
	}

	containers, err := deploymentContainers(obj)
	if err != nil {
		return err
	}
	if len(containers) == 0 {
		return errors.New("gatekeeper deployment has no containers")
	}

	idx := 0
	for i, container := range containers {
		if stringValue(container["name"]) == "manager" {
			idx = i
			break
		}
	}

	container := containers[idx]
	args := stringSlice(container["args"])
	flagNames := map[string]struct{}{
		"--enable-external-data":                      {},
		"--external-data-provider-response-cache-ttl": {},
	}

	newArgs := make([]string, 0, len(args)+2)
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		name := strings.SplitN(arg, "=", 2)[0]
		if _, ok := flagNames[name]; ok {
			if !strings.Contains(arg, "=") && name == "--external-data-provider-response-cache-ttl" {
				skipNext = true
			}
			continue
		}
		newArgs = append(newArgs, arg)
	}
	newArgs = append(newArgs, "--enable-external-data=true", "--external-data-provider-response-cache-ttl=0s")

	op := "add"
	if _, ok := container["args"]; ok {
		op = "replace"
	}
	patch := []map[string]any{{
		"op":    op,
		"path":  fmt.Sprintf("/spec/template/spec/containers/%d/args", idx),
		"value": newArgs,
	}}
	return writeJSONFile(patchPath, patch)
}

func deploymentContainers(obj map[string]any) ([]map[string]any, error) {
	spec, ok := asMap(obj["spec"])
	if !ok {
		return nil, errors.New("deployment is missing spec")
	}
	template, ok := asMap(spec["template"])
	if !ok {
		return nil, errors.New("deployment is missing spec.template")
	}
	templateSpec, ok := asMap(template["spec"])
	if !ok {
		return nil, errors.New("deployment is missing spec.template.spec")
	}
	containersAny, ok := templateSpec["containers"].([]any)
	if !ok {
		return nil, errors.New("deployment is missing spec.template.spec.containers")
	}
	containers := make([]map[string]any, 0, len(containersAny))
	for i, item := range containersAny {
		container, ok := asMap(item)
		if !ok {
			return nil, fmt.Errorf("container %d is not an object", i)
		}
		containers = append(containers, container)
	}
	return containers, nil
}

func writeGatekeeperValidatingWebhookPatch(vwcPath, patchPath string) error {
	var obj map[string]any
	if err := readJSONFile(vwcPath, &obj); err != nil {
		return err
	}

	webhooks, _ := obj["webhooks"].([]any)
	patches := []map[string]any{}
	for wi, webhookAny := range webhooks {
		webhook, ok := asMap(webhookAny)
		if !ok {
			continue
		}
		rules, _ := webhook["rules"].([]any)
		for ri, ruleAny := range rules {
			rule, ok := asMap(ruleAny)
			if !ok {
				continue
			}
			ops := stringSlice(rule["operations"])
			if contains(ops, "*") || contains(ops, "DELETE") {
				continue
			}
			ops = append(append([]string{}, ops...), "DELETE")
			patches = append(patches, map[string]any{
				"op":    "replace",
				"path":  fmt.Sprintf("/webhooks/%d/rules/%d/operations", wi, ri),
				"value": ops,
			})
		}
	}
	return writeJSONFile(patchPath, patches)
}

func writeApprovalConstraintPatch(publicKeyPath, patchPath string) error {
	publicKey, err := os.ReadFile(publicKeyPath)
	if err != nil {
		return err
	}
	patch := map[string]any{
		"spec": map[string]any{
			"match": map[string]any{
				"kinds": []map[string]any{
					{
						"apiGroups": []string{""},
						"kinds":     []string{"ConfigMap"},
					},
				},
				"namespaces": []string{"ci-approval"},
			},
			"parameters": map[string]any{
				"publicKey": string(publicKey),
			},
		},
	}
	return writeJSONFile(patchPath, patch)
}

func writeManualTouchAuditEvent(payloadPath, userAgent string) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000000Z07:00")
	payload := map[string]any{
		"items": []map[string]any{
			{
				"auditID": "ci-manual-touch-0001",
				"stage":   "ResponseComplete",
				"verb":    "patch",
				"user": map[string]any{
					"username": "ci-user",
					"groups":   []string{"system:authenticated"},
				},
				"userAgent": userAgent,
				"sourceIPs": []string{"127.0.0.1"},
				"objectRef": map[string]any{
					"resource":   "deployments",
					"namespace":  "ci-manual",
					"name":       "touched",
					"apiGroup":   "apps",
					"apiVersion": "v1",
				},
				"requestReceivedTimestamp": now,
			},
		},
	}
	return writeJSONFile(payloadPath, payload)
}

func stringValue(value any) string {
	s, _ := value.(string)
	return s
}

func stringSlice(value any) []string {
	raw, ok := value.([]any)
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func asMap(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
