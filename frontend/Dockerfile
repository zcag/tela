# syntax=docker/dockerfile:1

FROM node:24-alpine AS builder
WORKDIR /app
COPY package.json package-lock.json ./
# Cache mount persists npm's download cache across builds, so a package-lock
# change re-fetches only the changed tarballs instead of the whole tree.
RUN --mount=type=cache,target=/root/.npm npm ci
COPY . .
RUN npm run build

FROM nginx:alpine AS runtime
# nginx.conf is an envsubst template (the image renders /etc/nginx/templates on
# start), so the backend address can follow the deploy: the compose default is
# the Docker name below; a host without Docker DNS (ECS, a bare VM) sets
# TELA_BACKEND to an address it can reach (e.g. 127.0.0.1:8080) and TELA_RESOLVER
# to its DNS server. Only TELA_* is substituted, so nginx's own $vars survive.
ENV TELA_BACKEND=backend:8080 \
    TELA_RESOLVER=127.0.0.11 \
    NGINX_ENVSUBST_FILTER=^TELA_
COPY nginx.conf /etc/nginx/templates/default.conf.template
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
