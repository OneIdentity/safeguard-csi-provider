FROM gcr.io/distroless/static
ARG ARCH
COPY ./_output/${ARCH}/safeguard-csi-provider /bin/

LABEL description="Secrets Store CSI Driver Provider Safeguard"

ENTRYPOINT ["safeguard-csi-provider"]
