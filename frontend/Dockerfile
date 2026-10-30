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
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=builder /app/dist /usr/share/nginx/html
EXPOSE 80
