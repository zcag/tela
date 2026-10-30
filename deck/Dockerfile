# tela deck render sidecar — Slidev + headless Chromium for export.
FROM node:24-slim

ENV PORT=3344 \
    DECK_CACHE=/data \
    PLAYWRIGHT_BROWSERS_PATH=/ms-playwright \
    PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1

WORKDIR /app

# Chromium (+ system deps) in its OWN early layer, keyed only to the pinned
# Playwright version — so changing app deps (slidev/tahta/echarts/…) no longer
# invalidates and re-pushes this ~340MB layer. Keep this version in lockstep with
# the `playwright-chromium` pin in package.json so the browser revision matches.
RUN npx --yes playwright@1.60.0 install --with-deps chromium

# App deps + code: the layers that actually change on a normal deploy. The
# browser download is skipped (env above) — playwright-chromium reuses the
# Chromium installed above at PLAYWRIGHT_BROWSERS_PATH.
COPY package.json package-lock.json* ./
RUN npm install --omit=dev

COPY . .

VOLUME /data
EXPOSE 3344
CMD ["node", "server.mjs"]
