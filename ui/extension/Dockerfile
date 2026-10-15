# Builds the Argo CD UI extension without a host Node installation.
#   docker build -f ui/extension/Dockerfile -o ui/extension/dist ui/extension
FROM docker.io/library/node:22 AS build
WORKDIR /work
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
RUN npm run type-check && npm run lint && npm test && npm run build

# Layout expected by argocd-extension-installer (resources/*).
FROM scratch AS dist
COPY --from=build /work/dist/extension-terraform.js /resources/extension-terraform.js
