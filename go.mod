module github.com/OneIdentity/safeguard-csi-provider

go 1.16

require (
	github.com/pkg/errors v0.9.1
	golang.org/x/net v0.0.0-20210520170846-37e1c6afe023
	google.golang.org/grpc v1.38.0
	k8s.io/component-base v0.22.0
	k8s.io/klog/v2 v2.9.0
	sigs.k8s.io/secrets-store-csi-driver v0.3.0
)

require (
	github.com/google/uuid v1.3.0
	google.golang.org/genproto v0.0.0-20210602131652-f16073e35f0c
)
