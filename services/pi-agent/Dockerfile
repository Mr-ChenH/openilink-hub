FROM node:22-bookworm-slim AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY tsconfig.json ./
COPY src ./src
COPY test ./test
RUN npm run build && npm prune --omit=dev

FROM node:22-bookworm-slim
ENV NODE_ENV=production \
    HOST=0.0.0.0 \
    PORT=8080 \
    PI_AGENT_DIR=/var/lib/pi-agent/config \
    PI_AGENT_SESSION_DIR=/var/lib/pi-agent/sessions \
    PI_AGENT_WORKSPACE_DIR=/var/lib/pi-agent/workspace
WORKDIR /app
COPY --from=build --chown=node:node /app/package.json /app/package-lock.json ./
COPY --from=build --chown=node:node /app/node_modules ./node_modules
COPY --from=build --chown=node:node /app/dist/src ./dist/src
RUN mkdir -p /var/lib/pi-agent/config /var/lib/pi-agent/sessions /var/lib/pi-agent/workspace && chown -R node:node /var/lib/pi-agent
USER node
EXPOSE 8080
CMD ["node", "dist/src/index.js"]
