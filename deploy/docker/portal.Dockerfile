# syntax=docker/dockerfile:1
#
# portal.Dockerfile - the public portal (deploy/portal)
#
# Build context: the repository root (portal.Dockerfile.dockerignore lets
# only deploy/portal through). The module uses the standard library alone.

FROM --platform=$BUILDPLATFORM golang:1.27.4 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src/deploy/portal
COPY deploy/portal ./
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -o /rootfs/portal .

# 10001 owns the stack's volumes; the portal reads the provisioning token
# from admin-secrets, which the server image's provision job writes.
FROM gcr.io/distroless/static-debian12
COPY --from=build --chown=10001:10001 /rootfs/portal /portal
USER 10001:10001
ENTRYPOINT ["/portal"]
