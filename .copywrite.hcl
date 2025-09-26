schema_version = 1

project {
  name           = "terraform-provider-logfire"
  license        = "MPL-2.0"
  copyright_holder = "Pydantic, Inc."
  copyright_year   = 2025

  header_ignore = [
    # Documentation examples and prose
    "examples/**",
    "docs/**",

    # Repo/CI tooling
    ".github/**",
    ".golangci.yml",
    ".goreleaser.yml",
    ".pre-commit-config.yaml"
  ]
}
