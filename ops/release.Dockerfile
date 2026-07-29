FROM alpine:3.22

RUN apk add --no-cache ca-certificates
COPY docklane /usr/local/bin/docklane

EXPOSE 4646
ENTRYPOINT ["/usr/local/bin/docklane"]
