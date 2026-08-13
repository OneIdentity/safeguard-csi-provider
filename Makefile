-include secrets.env
export $(shell test -f secrets.env && sed 's/=.*//' secrets.env)

ORG_PATH=github.com/OneIdentity
PROJECT_NAME := safeguard-csi-provider
REPO_PATH="$(ORG_PATH)/$(PROJECT_NAME)"

REGISTRY_NAME ?= starlingdev
REPO_PREFIX ?= oneidentity/secrets-store
REGISTRY ?= $(REGISTRY_NAME).azurecr.io/$(REPO_PREFIX)
IMAGE_VERSION ?= v0.2.0
IMAGE_NAME ?= provider-safeguard
IMAGE_TAG := $(REGISTRY)/$(IMAGE_NAME):$(IMAGE_VERSION)

# build variables
BUILD_DATE=$$(date +%Y-%m-%d-%H:%M)
BUILD_COMMIT := $$(git rev-parse --short HEAD)
BUILD_DATE_VAR := $(REPO_PATH)/pkg/version.BuildDate
BUILD_VERSION_VAR := $(REPO_PATH)/pkg/version.BuildVersion
VCS_VAR := $(REPO_PATH)/pkg/version.Vcs
LDFLAGS ?= "-X $(BUILD_DATE_VAR)=$(BUILD_DATE) -X $(BUILD_VERSION_VAR)=$(IMAGE_VERSION) -X $(VCS_VAR)=$(BUILD_COMMIT)"

GO_FILES=$(shell go list ./... | grep -v /test/e2e)
ALL_DOCS := $(shell find . -name '*.md' -type f | sort)
TOOLS_MOD_DIR := ./tools
TOOLS_DIR := $(abspath ./.tools)

GO111MODULE = on
DOCKER_CLI_EXPERIMENTAL = enabled
export GOPATH GOBIN GO111MODULE DOCKER_CLI_EXPERIMENTAL

# Generate all combination of all OS, ARCH, and OSVERSIONS for iteration
ALL_OS = linux windows
ALL_ARCH.linux = amd64 arm64
ALL_OS_ARCH.linux = $(foreach arch, ${ALL_ARCH.linux}, linux-$(arch))
ALL_ARCH.windows = amd64
ALL_OSVERSIONS.windows := ltsc2022 ltsc2025
ALL_OS_ARCH.windows = $(foreach arch, $(ALL_ARCH.windows), $(foreach osversion, ${ALL_OSVERSIONS.windows}, windows-${osversion}-${arch}))
ALL_OS_ARCH = $(foreach os, $(ALL_OS), ${ALL_OS_ARCH.${os}})

# The current context of image building
# The architecture of the image
ARCH ?= amd64
# OS Version for the Windows images: ltsc2022 (Windows Server 2022),
# ltsc2025 (Windows Server 2025). The Semi-Annual Channel builds (1809, 1903,
# 1909, 2004) are out of servicing and are no longer built.
OSVERSION ?= ltsc2022
# Output type of docker buildx build
OUTPUT_TYPE ?= registry
BUILDKIT_VERSION ?= v0.8.1

# E2E test variables
KIND_VERSION ?= 0.11.0
KIND_K8S_VERSION ?= v1.21.2

$(TOOLS_DIR)/golangci-lint: $(TOOLS_MOD_DIR)/go.mod $(TOOLS_MOD_DIR)/go.sum $(TOOLS_MOD_DIR)/tools.go
	cd $(TOOLS_MOD_DIR) && \
	go build -o $(TOOLS_DIR)/golangci-lint github.com/golangci/golangci-lint/cmd/golangci-lint

$(TOOLS_DIR)/misspell: $(TOOLS_MOD_DIR)/go.mod $(TOOLS_MOD_DIR)/go.sum $(TOOLS_MOD_DIR)/tools.go
	cd $(TOOLS_MOD_DIR) && \
	go build -o $(TOOLS_DIR)/misspell github.com/client9/misspell/cmd/misspell

.PHONY: lint
lint: $(TOOLS_DIR)/golangci-lint $(TOOLS_DIR)/misspell
	$(TOOLS_DIR)/golangci-lint run --timeout=5m -v
	$(TOOLS_DIR)/misspell $(ALL_DOCS)

.PHONY: unit-test
unit-test:
	CGO_ENABLED=1 go test -race -coverprofile=coverage.txt -covermode=atomic $(GO_FILES) -v

.PHONY: build
build:
	CGO_ENABLED=0 GOARCH=${ARCH} GOOS=linux go build -a -ldflags ${LDFLAGS} -o _output/${ARCH}/safeguard-csi-provider ./cmd/

.PHONY: build-windows
build-windows:
	CGO_ENABLED=0 GOARCH=${ARCH} GOOS=windows go build -a -ldflags ${LDFLAGS} -o _output/${ARCH}/safeguard-csi-provider.exe ./cmd/

.PHONY: build-darwin
build-darwin:
	CGO_ENABLED=0 GOARCH=${ARCH} GOOS=darwin go build -a -ldflags ${LDFLAGS} -o _output/${ARCH}/safeguard-csi-provider ./cmd/

.PHONY: container
container: build
	docker build --no-cache --build-arg ARCH=$(ARCH) -t $(IMAGE_TAG) -f Dockerfile .

.PHONY: container-linux
container-linux: docker-buildx-builder
	docker buildx build \
			--no-cache \
			--output=type=$(OUTPUT_TYPE) \
			--platform="linux/$(ARCH)" \
			--build-arg ARCH=$(ARCH) \
			-t $(IMAGE_TAG)-linux-$(ARCH) -f Dockerfile .

.PHONY: container-windows
container-windows: docker-buildx-builder
	docker buildx build \
			--no-cache \
			--output=type=$(OUTPUT_TYPE) \
			--platform="windows/amd64" \
			--build-arg ARCH=$(ARCH) \
			--build-arg OSVERSION=$(OSVERSION) \
	 		-t $(IMAGE_TAG)-windows-$(OSVERSION)-$(ARCH) -f windows.Dockerfile .

.PHONY: docker-buildx-builder
docker-buildx-builder:
	@if ! DOCKER_CLI_EXPERIMENTAL=enabled docker buildx ls | grep -q container-builder; then\
		DOCKER_CLI_EXPERIMENTAL=enabled docker buildx create --name container-builder --use --driver-opt image=moby/buildkit:$(BUILDKIT_VERSION);\
	fi

.PHONY: container-all
container-all: build-windows
	for arch in $(ALL_ARCH.linux); do \
		ARCH=$${arch} $(MAKE) build; \
		ARCH=$${arch} $(MAKE) container-linux; \
	done
	for osversion in $(ALL_OSVERSIONS.windows); do \
		OSVERSION=$${osversion} $(MAKE) container-windows; \
	done

.PHONY: push-manifest
push-manifest:
	docker manifest create --amend $(IMAGE_TAG) $(foreach osarch, $(ALL_OS_ARCH), $(IMAGE_TAG)-${osarch})
	# add "os.version" field to windows images (based on https://github.com/kubernetes/kubernetes/blob/master/build/pause/Makefile)
	set -x; \
	registry_prefix=$(shell (echo ${REGISTRY} | grep -Eq ".*\/.*") && echo "" || echo "docker.io/"); \
	manifest_image_folder=`echo "$${registry_prefix}${IMAGE_TAG}" | sed "s|/|_|g" | sed "s/:/-/"`; \
	for arch in $(ALL_ARCH.windows); do \
		for osversion in $(ALL_OSVERSIONS.windows); do \
			BASEIMAGE=mcr.microsoft.com/windows/nanoserver:$${osversion}; \
			full_version=`docker manifest inspect $${BASEIMAGE} | jq -r '.manifests[0].platform["os.version"]'`; \
			sed -i -r "s/(\"os\"\:\"windows\")/\0,\"os.version\":\"$${full_version}\"/" "${HOME}/.docker/manifests/$${manifest_image_folder}/$${manifest_image_folder}-windows-$${osversion}-$${arch}"; \
		done; \
	done
	docker manifest push --purge $(IMAGE_TAG)
	docker manifest inspect $(IMAGE_TAG)

.PHONY: clean
clean:
	go clean -r -x
	-rm -rf _output

.PHONY: mod
mod:
	@go mod tidy