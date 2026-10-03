# Runtime and trust boundaries

The host and CLI are one compiled Go executable. A project-local, verified Go
toolchain avoids changing the machine PATH. The daemon uses standard HTTP,
bounded admission and streaming files. MCP uses the official Go SDK and runs
only when an agent connects. The dashboard is static HTML/CSS/JavaScript served
on demand; it has no desktop shell or permanent browser process.

The first worker is dependency-free Python in Termux. Its authenticated HTTP
protocol is independent of Termux and model/manufacturer. A mock worker uses
the identical executor/protocol but is explicitly labeled simulated hardware.
Mock execution runs on this workstation and cannot prove Android performance.

USB ADB tunnels are the default. Only explicitly approved serials are contacted.
Pairing stores a random worker secret separately from device profiles. Files
and commands reach only the dedicated worker workspace, not personal storage.
Jobs are trusted developer code, not a hostile-code sandbox. Android does not
provide general network isolation to an unprivileged Termux process; a strict
network-denial requirement is rejected, never silently promised.

Disk-backed JSON job records, append-only audit records, and bounded moving
history replace SQLite initially to minimize dependencies. Profiles and config
are atomically replaced; unfinished attempts become INTERRUPTED on recovery.
Non-idempotent work is never automatically replayed. Full SQLite migration and
a native Android foreground service remain later engineering work.
