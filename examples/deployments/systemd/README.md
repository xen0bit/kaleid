# Kaleid as a systemd service

This runs the `kaleid` binary under systemd, with configuration in
`/etc/kaleid/kaleid.env`. The comment at the top of
[`kaleid.service`](kaleid.service) has the install steps. Kaleid keeps no
local state, so the unit uses a strict sandbox and needs no writable
directories.

Two things to set up first:

- **PostgreSQL 17** with pgvector 0.8 or newer. Kaleid's role must be able to
  create the `vector` and `pg_trgm` extensions and create schemas. See
  [docs/deployment.md](../../../docs/deployment.md).
- **Tokens** in `/etc/kaleid/tokens.json` if you use token auth. See
  [docs/configuration.md](../../../docs/configuration.md#authentication).

Once the service is running:

```sh
systemctl status kaleid
journalctl -u kaleid -f
curl localhost:8000/api/v2/healthcheck
```
