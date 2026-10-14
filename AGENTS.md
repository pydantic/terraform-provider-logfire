# Verification

Read [CONTRIBUTING.md](CONTRIBUTING.md) before changing this provider.
Use [verify-terraform-provider-logfire](.cursor/skills/verify-terraform-provider-logfire/SKILL.md)
and its feature map to select checks for the changed behavior before claiming
completion. Save the commands, exit codes, and observable results outside the
checkout. Report skipped checks and unmet prerequisites.

Local Terraform CLI tests use temporary HTTP APIs. They do not prove compatibility
with a running Logfire instance. Live acceptance tests create and delete resources.
Use an explicitly selected disposable target and credentials authorized for it.
Keep state, API keys, and token outputs out of commits and shared evidence.
