# annotation-check — voiceover

**1. Scenario.** Every Namespace that opts into the contract must
carry an `earlywatch.io/owner` annotation matching a regex. This is
the Azure-shipped built-in policy ("Kubernetes cluster resources
should have the specified annotations"), not a custom Rego. Useful
narrative beat: show that EarlyWatch-style governance composes with
out-of-the-box Azure policies, not just the custom set.

**2. The assignment.** `policyDefinitionId` points to the built-in;
the parameters are `kind=Namespace`,
`annotations={key: earlywatch.io/owner, allowedRegex: .+}`,
`effect=Deny`. The `labelSelector` narrowing
(`earlywatch.io/anno-check=true`) is critical — without it, the
constraint would intercept *every* `kubectl create ns` in the cluster
and break unrelated demos.

**3. BAD.** `bad.yaml` defines a Namespace that carries the opt-in
label but no owner annotation. CREATE is denied. `kubectl get ns`
confirms it never landed.

**4. GOOD.** `good.yaml` carries the same opt-in label plus
`earlywatch.io/owner=team-a`. The annotation matches `.+`, the
constraint allows, the Namespace is created.

> Note: this built-in rule also runs at audit time against existing
> objects — but in this demo we only assert the admission path. Audit
> findings are out of scope.
