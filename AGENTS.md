# Verification

Read [CONTRIBUTING.md](CONTRIBUTING.md) before changing this provider.
Use [verify-terraform-provider-logfire](.cursor/skills/verify-terraform-provider-logfire/SKILL.md)
and its feature map to select checks for the changed behavior before claiming
completion. Save the commands, exit codes, and observable results outside the
checkout. Report skipped checks and unmet prerequisites.

For API-backed changes, compare the affected behavior with platform's public
`api.json` and run the existing local resource lifecycle tests. Record which
platform revision the comparison covers. The provider has no automatic OpenAPI
client generation or platform version pin. Live acceptance is an additional check
when the change needs runtime API evidence. It is not required for every change.
Use an authorized disposable target for it. Keep state and tokens private.
