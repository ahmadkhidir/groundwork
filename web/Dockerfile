# syntax=docker/dockerfile:1

# Groundwork web client image (Fly.io app "groundwork-web").
#
# The Next.js app is built with `output: "standalone"` and served by its minimal node server.
# NEXT_PUBLIC_* values are baked into the client bundle AT BUILD TIME — they are passed as build
# args (web/fly.toml [build.args]) and can never be changed at runtime. Server-only values
# (AUTH_SECRET, AUTH_URL) are runtime env: AUTH_SECRET is a Fly secret, and the build uses only a
# throwaway placeholder so no real secret ever lands in an image layer (the prerendered static
# pages are unauthenticated — they never contain session data).

FROM node:22-alpine AS deps
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY . .

ARG NEXT_PUBLIC_API_BASE_URL
ARG NEXT_PUBLIC_MAX_UPLOAD_BYTES
ARG NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY
ENV NEXT_PUBLIC_API_BASE_URL=$NEXT_PUBLIC_API_BASE_URL \
    NEXT_PUBLIC_MAX_UPLOAD_BYTES=$NEXT_PUBLIC_MAX_UPLOAD_BYTES \
    NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY=$NEXT_PUBLIC_STRIPE_PUBLISHABLE_KEY \
    NEXT_TELEMETRY_DISABLED=1
# Build-time placeholder only (see header comment). The real secret is injected at runtime.
ENV AUTH_SECRET=build-time-placeholder-only
ENV AUTH_URL=http://localhost:3000

RUN npm run build

FROM node:22-alpine AS runner
WORKDIR /app
ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1

# Standalone server + the static assets it serves + the public dir (icon, favicon).
COPY --from=build /app/.next/standalone ./
COPY --from=build /app/.next/static ./.next/static
COPY --from=build /app/public ./public

EXPOSE 3000
CMD ["node", "server.js"]
