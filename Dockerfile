# The build context holds charmap binaries cross-compiled on the build host,
# one per platform at <os>/<arch>/charmap (scripts/build-binaries.sh), so no
# platform compiles or runs anything here.
#
# No shell or package manager: copy the binary into the image that runs it,
# e.g. COPY --from=docker.io/ashtonian/charmap:2 /usr/local/bin/charmap /usr/local/bin/
FROM gcr.io/distroless/static-debian13:nonroot@sha256:aafe993665bf6200db38a02c11a9351e782a84b6a45e7f33b6695638c88e3931
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/charmap /usr/local/bin/charmap
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/charmap"]
CMD ["help"]
