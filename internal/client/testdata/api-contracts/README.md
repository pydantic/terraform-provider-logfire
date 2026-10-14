# Recorded API-key contracts

The client tests validate actual API-key HTTP requests and successful responses
against every contract in `manifest.json`. The snapshots contain the two API-key
path items and all components they reference. The original `/api` server prefix,
request and response schemas, formats, and security declarations remain intact.

The selected operations cover creation, list-based reads, updates, and deletion.
Existing literal assertions still check the values that the client sends and
returns. Schema validation does not prove authorization, scope relationships,
server-side effects, or compatibility with an unrecorded release.

`api-keys-floor-v2026-06-09.02.json` records the first release with API-key creation.
The preceding `.01` release only lists, updates, and revokes API keys.
`self-hosted-v2026-10-07.02.json` comes from the exact platform commit used by
stable chart `0.14.1`. `saas-us.json` records the public US deployment's schema.
The manifest records full source hashes and extracted snapshot hashes separately.
The SaaS entry also records when its source first changed to those bytes.
OpenAPI's `info.version` is not the platform release identity.
Scopes and other optional features can have later minimum versions. Passing the
enrolled requests does not make every newer scope available at the API-key floor.

Tests read committed fixtures. They need no API credentials or network access.
Refresh the SaaS contract explicitly from the repository root:

```bash
python3 scripts/update-api-contracts.py --contract saas-us
```

Refresh the released contract from a platform checkout containing its recorded commit:

```bash
python3 scripts/update-api-contracts.py --contract self-hosted-v2026-10-07.02 \
  --platform-repo /absolute/path/to/platform
```

To record a new release, add its exact platform commit and release metadata to
the manifest before running the script. Review the schema diff and run the client
tests. Preserve documented resource feature floors and older-backend behavior
tests. A new snapshot does not change the provider's compatibility promises.

Enroll additional operations explicitly in the extractor and their client tests.
Keep deliberate errors and malformed-response tests outside the successful-response
validator. The published schemas currently omit the provider's SLO endpoints.
The existing SLO lifecycle and older-release tests cover their behavior separately.
