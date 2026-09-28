---
name: repo-development
description: "Modify, debug, or test pemcast's Go implementation while preserving its protocol, validation, atomic activation, and generated-config rules. Not for pure deployment operations."
---

# pemcast development

Use this skill for code and test changes. Read [references/development.md](references/development.md), then inspect the specific packages listed there before editing.

Minimum verification for Go changes is `go test ./...`. Configuration-schema changes must also regenerate and commit `config/config.example.yaml` through the existing config test.
