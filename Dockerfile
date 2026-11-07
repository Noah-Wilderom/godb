# GoReleaser supplies the prebuilt binaries in platform-specific directories.
FROM scratch

ARG TARGETPLATFORM

WORKDIR /var/lib/godb

COPY ${TARGETPLATFORM}/godb /usr/local/bin/godb

ENTRYPOINT ["/usr/local/bin/godb"]
