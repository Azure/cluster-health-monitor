REGISTRY ?= ghcr.io
ifndef TAG
	TAG ?= $(shell git rev-parse --short=7 HEAD)
endif
CLUSTER_HEALTH_MONITOR_IMAGE_VERSION ?= $(TAG)
CLUSTER_HEALTH_MONITOR_IMAGE_NAME ?= cluster-health-monitor

## --------------------------------------
## Build
## --------------------------------------

.PHONY: build
build: ## Build binaries.
	go build -o bin/clusterhealthmonitor ./cmd/clusterhealthmonitor

.PHONY: run-clusterhealthmonitor
run-clusterhealthmonitor: ## Run a controllers from your host.
	go run ./cmd/clusterhealthmonitor/main.go

## --------------------------------------
## Images
## --------------------------------------

OUTPUT_TYPE ?= type=registry
BUILDX_BUILDER_NAME ?= img-builder
QEMU_VERSION ?= 7.2.0-1
BUILDKIT_VERSION ?= v0.18.1

.PHONY: push
push:
	$(MAKE) OUTPUT_TYPE="type=registry" docker-build-cluster-health-monitor

# By default, docker buildx create will pull image moby/buildkit:buildx-stable-1 and hit the too many requests error
.PHONY: docker-buildx-builder
docker-buildx-builder:
	@if ! docker buildx ls | grep $(BUILDX_BUILDER_NAME); then \
		docker run --rm --privileged mcr.microsoft.com/mirror/docker/multiarch/qemu-user-static:$(QEMU_VERSION) --reset -p yes; \
		docker buildx create --driver-opt image=mcr.microsoft.com/oss/v2/moby/buildkit:$(BUILDKIT_VERSION) --name $(BUILDX_BUILDER_NAME) --use; \
		docker buildx inspect $(BUILDX_BUILDER_NAME) --bootstrap; \
	fi

.PHONY: docker-build-cluster-health-monitor
docker-build-cluster-health-monitor: docker-buildx-builder
	docker buildx build \
		--file docker/$(CLUSTER_HEALTH_MONITOR_IMAGE_NAME).Dockerfile \
		--output=$(OUTPUT_TYPE) \
		--platform="linux/amd64" \
		--pull \
		--tag $(REGISTRY)/$(CLUSTER_HEALTH_MONITOR_IMAGE_NAME):$(CLUSTER_HEALTH_MONITOR_IMAGE_VERSION) .

## --------------------------------------
## Local Test with Kind
## --------------------------------------

GIT_ROOT = $(shell git rev-parse --show-toplevel)
LOCAL_IMAGE_NAME = cluster-health-monitor
LOCAL_IMAGE_TAG = test-latest
KIND_CLUSTER_NAME ?= chm-test
KUBECONFIG ?= $(HOME)/.kube/config

.PHONY: kind-create-cluster
kind-create-cluster:
	@echo "Creating Kind cluster '$(KIND_CLUSTER_NAME)' with kubeconfig at $(KUBECONFIG)"
	@if ! kind get clusters | grep -q $(KIND_CLUSTER_NAME); then \
		kind create cluster --name $(KIND_CLUSTER_NAME) --kubeconfig $(KUBECONFIG); \
	else \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists."; \
	fi

.PHONY: kind-build-image
kind-build-image:
	docker build \
		--file ${GIT_ROOT}/docker/$(LOCAL_IMAGE_NAME).Dockerfile \
		--tag $(LOCAL_IMAGE_NAME):$(LOCAL_IMAGE_TAG) .

.PHONY: kind-load-image
kind-load-image: kind-build-image
	kind load docker-image $(LOCAL_IMAGE_NAME):$(LOCAL_IMAGE_TAG) --name $(KIND_CLUSTER_NAME)

.PHONY: kind-export-kubeconfig
kind-export-kubeconfig:
	kind export kubeconfig --name $(KIND_CLUSTER_NAME)

.PHONY: kind-apply-manifests
kind-apply-manifests: kind-export-kubeconfig
	kubectl apply -k ${GIT_ROOT}/manifests/overlays/test

.PHONY: kind-delete-deployment
kind-delete-deployment: kind-export-kubeconfig
	kubectl delete -k ${GIT_ROOT}/manifests/overlays/test

.PHONY: kind-redeploy
kind-redeploy: kind-delete-deployment kind-build-image kind-load-image kind-apply-manifests
	@echo "Redeployed cluster health monitor to Kind cluster '$(KIND_CLUSTER_NAME)'"

.PHONY: kind-delete-cluster
kind-delete-cluster:
	kind delete cluster --name $(KIND_CLUSTER_NAME)

.PHONY: kind-test-local
kind-test-local: kind-create-cluster kind-load-image kind-apply-manifests
	@echo "Cluster health monitor deployed to Kind cluster '$(KIND_CLUSTER_NAME)'"
	@echo "Use 'make kind-export-kubeconfig' to set the kubectl context for Kind cluster '$(KIND_CLUSTER_NAME)'"
	@echo "Use 'kubectl -n cluster-health-monitor get pods' to check the status"
	@echo "Use 'kubectl -n cluster-health-monitor port-forward deployment/cluster-health-monitor 9800' to access metrics"
	@echo "Use 'make kind-redeploy' to redeploy the cluster health monitor"
	@echo "Use 'make kind-delete-cluster' when you're done testing"

## -----------------------------------
## Cleanup
## -----------------------------------

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries
	rm -rf ./bin
