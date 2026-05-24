# AsyncQ demo

This demo runs:

- a local q process on port `5000`
- Grafana 13 on `http://localhost:3000`
- a provisioned `AsyncQ Demo` datasource
- a provisioned `AsyncQ kdb+ demo` dashboard with sync, async, and stream panels

## Start without Docker

From the repository root:

```bash
./scripts/start-demo-local.sh
```

Open:

```text
http://localhost:3000/d/asyncq-kdb-demo/asyncq-kdb-demo
```

Grafana is configured with anonymous admin access for the demo. The explicit login is `admin` / `admin`.

The local starter downloads Grafana OSS `13.0.1` into `demo/runtime/` on first run and keeps all Grafana data, logs, generated provisioning, and plugin symlinks under that ignored runtime directory.

## Start with Docker

Docker is optional. It is useful when you want a fully disposable Grafana container:

```bash
./scripts/start-demo-q.sh
./scripts/start-demo-grafana.sh
```

## What to try

- `Sync latest trades` calls `.demo.asyncq.latest 25`.
- `Async aggregate after queued delay` calls `.demo.asyncq.slowAgg[]` through the async helper functions. The demo q process waits about three seconds before marking the job done.
- `Streaming tick prices` and `Streaming rows` subscribe through Grafana Live. The q timer publishes five new rows every second to active streams.
- `Demo process counters` shows row, stream, and job counts from the q process.

If you restart the q process while the dashboard is already open, refresh the browser tab so the async and streaming panels create fresh Grafana Live subscriptions.

## Stop

```bash
./scripts/stop-demo-local.sh
```

For the Docker path, run `docker compose down` from `demo/`, then `./scripts/stop-demo-q.sh`.

## Files

- `demo/q/asyncq_demo.q` - q demo process
- `demo/grafana/provisioning/datasources/asyncq.yml` - datasource provisioning
- `demo/grafana/provisioning/dashboards/json/asyncq-demo.json` - dashboard
- `demo/docker-compose.yml` - Grafana 13 container

## Notes

The q process is intentionally permissive and evaluates demo query text. It is for local development only.
