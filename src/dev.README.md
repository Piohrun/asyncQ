## Developer notes

### Frontend

```bash
npm install
npm run typecheck
npm run build
npm run dev
```

### Backend

```bash
go test ./pkg/...
mage -l
mage -v
```

### Grafana development mode

Set Grafana to development mode and allow this unsigned plugin ID while iterating:

```ini
app_mode = development
allow_loading_unsigned_plugins = asyncq-kdbbackend-datasource
```

### q helper check

```bash
printf '\\\\\n' | q q/asyncq_grafana.q -q -T 5 -w 1024 -u 1 -b
```
