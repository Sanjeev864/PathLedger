# PathLedger dashboard

Svelte 5 + TypeScript + ECharts single-page app. `npm run build` writes to
`../internal/controller/webui/dist`, which the controller embeds and serves
at `/`.

```sh
npm ci
npm run dev     # http://localhost:5173, proxies /api to a controller on :8080
npm run check   # type-check
npm run build   # then rebuild the controller (make build)
```
