# The build context holds charmap binaries cross-compiled on the build host,
# one per platform at <os>/<arch>/charmap (scripts/build-binaries.sh), so no
# platform compiles or runs anything here.
#
# No shell or package manager: copy the binary into the image that runs it,
# e.g. COPY --from=docker.io/ashtonian/charmap:2 /usr/local/bin/charmap /usr/local/bin/
FROM gcr.io/distroless/static-debian13:nonroot@sha256:e2e927ec666bae08560abb3c55d0659eceabb657f56b6782ab500a9fc7f555e3
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/charmap /usr/local/bin/charmap
WORKDIR /work
ENTRYPOINT ["/usr/local/bin/charmap"]
CMD ["help"]
