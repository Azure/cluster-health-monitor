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
	go build -o bin/controller ./cmd/controller

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
PLATFORM ?= linux/amd64,linux/arm64

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
		--platform="$(PLATFORM)" \
		--pull \
		--tag $(REGISTRY)/$(CLUSTER_HEALTH_MONITOR_IMAGE_NAME):$(CLUSTER_HEALTH_MONITOR_IMAGE_VERSION) .

## -----------------------------------
## Tests
## -----------------------------------

.PHONY: test-e2e
test-e2e:
	ginkgo -v -p --race ./test/e2e/

.PHONY: test-unit
test-unit:
	go test --race $$(go list ./... | grep -v /e2e)

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
	@if kind get clusters | grep -q "^$(KIND_CLUSTER_NAME)$$"; then \
		echo "Kind cluster '$(KIND_CLUSTER_NAME)' already exists, skipping creation"; \
	else \
		echo "Creating Kind cluster '$(KIND_CLUSTER_NAME)' with 3 nodes"; \
		{ \
			echo "kind: Cluster"; \
			echo "apiVersion: kind.x-k8s.io/v1alpha4"; \
			echo "nodes:"; \
			echo "- role: control-plane"; \
			echo "- role: worker"; \
			echo "- role: worker"; \
		} | kind create cluster --name $(KIND_CLUSTER_NAME) --config=-; \
		echo "Configuring CoreDNS with 3 replicas spread across nodes"; \
		kubectl --context kind-$(KIND_CLUSTER_NAME) patch deployment coredns -n kube-system -p '{"spec":{"replicas":3,"strategy":{"type":"RollingUpdate","rollingUpdate":{"maxSurge":0,"maxUnavailable":1}},"template":{"spec":{"affinity":{"podAntiAffinity":{"requiredDuringSchedulingIgnoredDuringExecution":[{"labelSelector":{"matchLabels":{"k8s-app":"kube-dns"}},"topologyKey":"kubernetes.io/hostname"}]}}}}}}'; \
		kubectl --context kind-$(KIND_CLUSTER_NAME) rollout status deployment coredns -n kube-system --timeout=120s; \
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

.PHONY: kind-setup-e2e
kind-setup-e2e: kind-create-cluster kind-deploy-metrics-server kind-load-image

.PHONY: kind-deploy-metrics-server
kind-deploy-metrics-server: kind-export-kubeconfig
	@echo "Deploying metrics-server to Kind cluster"
	kubectl apply -f https://github.com/kubernetes-sigs/metrics-server/releases/latest/download/components.yaml
	kubectl patch deployment metrics-server -n kube-system --type='json' -p='[{"op": "add", "path": "/spec/template/spec/containers/0/args/-", "value": "--kubelet-insecure-tls"}]'
	@echo "Waiting for metrics-server to be ready"
	kubectl wait --for=condition=available --timeout=120s deployment/metrics-server -n kube-system

.PHONY: kind-test-local
kind-test-local: kind-setup-e2e kind-apply-manifests
	@echo "Cluster health monitor deployed to Kind cluster '$(KIND_CLUSTER_NAME)'"
	@echo "Use 'make kind-export-kubeconfig' to set the kubectl context for Kind cluster '$(KIND_CLUSTER_NAME)'"
	@echo "Use 'kubectl -n kube-system get pods' to check the status"
	@echo "Use 'kubectl -n kube-system port-forward deployment/cluster-health-monitor 9800' to access metrics"
	@echo "Use 'make kind-redeploy' to redeploy the cluster health monitor"
	@echo "Use 'make kind-delete-cluster' when you're done testing"

## -----------------------------------
## Cleanup
## -----------------------------------

.PHONY: clean-bin
clean-bin: ## Remove all generated binaries
	rm -rf ./bin
