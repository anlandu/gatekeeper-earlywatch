# expression-check — voiceover

One bullet per `wait` checkpoint. Speak it, then press ENTER through the
typed commands until the next `wait`.

**1. Scenario.** A platform team wants to block destructive ops on
resources whose namespace or name matches a regex chosen at assignment
time. One policy definition, many possible per-environment regexes —
the rule code has no literals. Same Rego covers "don't delete anything
in prod" and "don't delete anything called `critical-*`" without a
code change.

**2. The assignment.** `kinds=[ConfigMap]`, `operations=[DELETE]`,
`namespacePattern=^prod.*`, `namePattern=^critical-.*`. Note the
`namespaces` list — we narrowed the rule to `prod-eu` plus
`ew-expression-check` so the other rules in this cluster don't fire on
the same objects. The deny condition is "namespace matches OR name
matches" — either regex alone is enough.

**3. BAD.** Apply the bad fixture: a ConfigMap `cm-1` in `prod-eu`.
The `kubectl delete` is intercepted by the Gatekeeper webhook; the
namespace matches `^prod.*` so the constraint's deny path fires. The
error response includes the constraint name. `kubectl get` confirms
the ConfigMap still exists — admission denial, no partial state.

**4. GOOD.** Apply the good fixture: a ConfigMap `cm-ok` in
`ew-expression-check`. Namespace doesn't match `^prod.*`, name doesn't
match `^critical-.*`, so neither half of the OR is true — no
violation, the DELETE goes through. Same policy, same cluster, same
constraint; only the object we touched changed.
