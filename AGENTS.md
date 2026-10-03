# Tidal Bridge development

Speak English with the user. Use scripts/build.ps1 to build the compiled host.
Use .tools/go/bin/go.exe test ./... and python tests/e2e/run.py for meaningful
policy/protocol/execution checks. Tests never require a physical Android node.

The host is the control plane; devices remain separate machines. Keep scheduler
types model-agnostic and version the protocol. Never send secrets, Windows
dependency directories, or incompatible native output to workers. Never replay
destructive/non-idempotent work. Mock workers run on this host and must always
be labeled simulated. Preserve low idle overhead and bounded memory/queues.

Before resource-heavy work, check Tidal Bridge availability and route economics.
Use its MCP tools when loaded. If they are absent, use bin/tidalbridge.exe status
and route with an absolute workspace and a realistic local duration estimate.
For compatible beneficial REMOTE placement, use run with the same options.
Remote placement uses an eligible worker ID in the route's target field.
Approved project command adapters may submit supported ordinary commands
automatically; use their environment rather than bypassing with absolute
runtime paths. Commands outside approved profiles remain local.
If the CLI/daemon is unavailable or placement is LOCAL, continue locally.
Only declare a job idempotent when replay is safe. After submission, do not
duplicate a job locally on a polling error. Follow SECURITY.md.
