# expression-check

EarlyWatch's [`ExpressionCheck`](https://github.com/brendandburns/early-watch/blob/main/docs/rule-types/expression-check.md)
is a minimal DSL: `field == 'value'` over `operation`, `namespace`, and `name`.
It is strictly less capable than either CEL or Rego.

## Why this isn't a single parametric template

A previous iteration of this folder exposed a generic `EWExpressionCheck`
template with a structured predicate schema (`operationIn`, `namespaceIn`,
`namespaceRegex`, `nameIn`, `nameRegex`). That was both over-built (regex and
multi-rule arrays exceed EarlyWatch's grammar) and still less expressive than
just writing CEL.

The only reason to invent a predicate schema would be to evaluate
EarlyWatch's expression string at admission time. Neither CEL nor Rego
supports `eval()` on a string supplied as a constraint parameter — Gatekeeper
compiles CEL and Rego at template creation. A "generic" template therefore
cannot remain generic without re-implementing an expression evaluator in
Rego.

## The recommended pattern

For each EarlyWatch `ExpressionCheck` rule, author a tiny dedicated
`ConstraintTemplate` whose predicate is the CEL translation of the
EarlyWatch expression.

[`template.yaml`](template.yaml) is the canonical example, derived from
EarlyWatch's documented `block-delete-in-production` sample. The predicate is
a single CEL line:

```cel
!(request.operation == 'DELETE' && request.namespace == 'production')
```

To support additional EarlyWatch rules, copy this folder, rename the
`ConstraintTemplate` kind, and replace the CEL expression. The match scope
(kinds, namespaces, label selectors) lives on the constraint, not the
template, so the template stays tiny.

## Translation cheat sheet

| EarlyWatch expression           | CEL                                          |
|---------------------------------|----------------------------------------------|
| `operation == 'DELETE'`         | `request.operation == 'DELETE'`              |
| `namespace == 'production'`     | `request.namespace == 'production'`          |
| `name == 'foo'`                 | `request.name == 'foo'`                      |

The Gatekeeper validation expression is the *allow* form, i.e. wrap the
EarlyWatch deny expression in `!(...)`.

## Going beyond EarlyWatch

Because each rule is plain CEL, you also get the full CEL standard library
for free — `matches`, `startsWith`, `contains`, `int`, etc. — none of which
EarlyWatch's grammar supports. [`template-regex.yaml`](template-regex.yaml)
and [`constraint-regex.yaml`](constraint-regex.yaml) ship a second example
that uses `request.namespace.matches('^prod.*')` and
`request.name.matches('^critical-.*')` to deny DELETE in any production-like
namespace or for any critical-prefixed resource. Use this as the template for
any rule that needs a richer predicate than `field == 'value'`.
