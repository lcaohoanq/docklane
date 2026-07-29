FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce

ARG TARGETARCH
ARG VERSION
ARG REVISION

RUN apk add --no-cache ca-certificates
COPY "linux/${TARGETARCH}/docklane" /usr/local/bin/docklane

LABEL org.opencontainers.image.title="Docklane"
LABEL org.opencontainers.image.description="Local HTTPS gateway controller and restricted probe"
LABEL org.opencontainers.image.source="https://github.com/lcaohoanq/docklane"
LABEL org.opencontainers.image.url="https://github.com/lcaohoanq/docklane"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${REVISION}"

EXPOSE 4646
ENTRYPOINT ["/usr/local/bin/docklane"]
