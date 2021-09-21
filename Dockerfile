FROM gcr.io/distroless/static
ARG ARCH
COPY ./_output/${ARCH}/secrets-store-csi-driver-provider-azure /bin/

LABEL description="Secrets Store CSI Driver Provider Safeguard"

ENTRYPOINT ["safeguard-csi-provider"]
