# syntax=docker/dockerfile:1.7
#
# Multi-stage build for Vue (Vite) frontend, served by Nginx.
#
# Build:
#   docker build -f frontend/Dockerfile -t gofeed-frontend .
#

FROM node:24-alpine AS build
RUN npm install -g pnpm@11.18.0
WORKDIR /src

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile

COPY frontend/ ./
RUN pnpm run build

FROM nginx:1.27-alpine
COPY frontend/nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /src/dist /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
