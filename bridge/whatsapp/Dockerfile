FROM node:20-slim

WORKDIR /app

# git required for the libsignal-node GitHub dependency in Baileys.
# The package-lock resolves it as git+ssh; sed rewrites to git+https so no SSH key is needed.
RUN apt-get update && \
    apt-get install -y --no-install-recommends git && \
    rm -rf /var/lib/apt/lists/*

COPY package.json package-lock.json ./
RUN sed -i 's|git+ssh://git@github.com/|git+https://github.com/|g' package-lock.json && \
    npm ci --omit=dev

COPY server.js ./

# Auth state persisted via volume at /app/auth_info
VOLUME ["/app/auth_info"]

ENV BRIDGE_PORT=3001
ENV AUTH_DIR=/app/auth_info
ENV LOG_LEVEL=silent

EXPOSE 3001

CMD ["node", "server.js"]
