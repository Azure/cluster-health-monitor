#!/bin/bash

# Script to provision an AKS cluster and deploy cluster-health-monitor
# Usage: ./hack/provision-aks-and-deploy.sh --subscription <subscription-id> --location <location> --resource-group <rg-name> --cluster-name <cluster-name> --acr-resource-id <acr-resource-id>
# Note: Run this script from the project root directory

set -euo pipefail

# Color codes for output
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Default values
SUBSCRIPTION=""
LOCATION=""
RESOURCE_GROUP=""
CLUSTER_NAME=""
ACR_RESOURCE_ID=""
NODE_SIZE="Standard_DS2_v2"
KUBERNETES_VERSION=""
CLEANUP=false
AKS_AUTOMATIC=false

# Function to print colored output
print_status() {
    echo -e "${BLUE}[INFO]${NC} $1"
}

print_success() {
    echo -e "${GREEN}[SUCCESS]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# Function to show usage
usage() {
    cat << EOF
Usage: $0 [OPTIONS]

Required options:
    --subscription, -s      Azure subscription ID
    --location, -l          Azure region/location (e.g., eastus, westus2)
    --resource-group, -g    Resource group name
    --cluster-name, -n      AKS cluster name
    --acr-resource-id       Azure Container Registry resource ID

Optional options:
    --node-size             VM size for nodes (default: Standard_DS2_v2)
    --kubernetes-version    Kubernetes version (default: latest supported)
    --aks-automatic         Create an AKS Automatic cluster
    --cleanup               Delete the resource group and all resources
    --help, -h              Show this help message

Examples:
    # Create cluster and deploy
    $0 --subscription "12345678-1234-1234-1234-123456789012" \\
       --location "eastus" \\
       --resource-group "my-rg" \\
       --cluster-name "my-aks-cluster" \\
       --acr-resource-id "/subscriptions/12345678-1234-1234-1234-123456789012/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myacr"

    # Create cluster with custom configuration and deploy
    $0 --subscription "12345678-1234-1234-1234-123456789012" \\
       --location "westus2" \\
       --resource-group "my-rg" \\
       --cluster-name "my-cluster" \\
       --acr-resource-id "/subscriptions/12345678-1234-1234-1234-123456789012/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myacr" \\
       --node-size "Standard_DS3_v2" \\
       --kubernetes-version "1.28.0"

    # Create AKS Automatic cluster and deploy
    $0 --subscription "12345678-1234-1234-1234-123456789012" \\
       --location "eastus" \\
       --resource-group "my-rg" \\
       --cluster-name "my-aks-cluster" \\
       --acr-resource-id "/subscriptions/12345678-1234-1234-1234-123456789012/resourceGroups/my-rg/providers/Microsoft.ContainerRegistry/registries/myacr" \\
       --aks-automatic

    # Delete cluster resource group and associated resources
    $0 --subscription "12345678-1234-1234-1234-123456789012" \\
       --location "eastus" \\
       --resource-group "my-rg" \\
       --cluster-name "my-aks-cluster" \\
       --cleanup

EOF
}

# Parse command line arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --subscription|-s)
            SUBSCRIPTION="$2"
            shift 2
            ;;
        --location|-l)
            LOCATION="$2"
            shift 2
            ;;
        --resource-group|-g)
            RESOURCE_GROUP="$2"
            shift 2
            ;;
        --cluster-name|-n)
            CLUSTER_NAME="$2"
            shift 2
            ;;
        --acr-resource-id)
            ACR_RESOURCE_ID="$2"
            shift 2
            ;;
        --node-size)
            NODE_SIZE="$2"
            shift 2
            ;;
        --kubernetes-version)
            KUBERNETES_VERSION="$2"
            shift 2
            ;;
        --cleanup)
            CLEANUP=true
            shift
            ;;
        --aks-automatic)
            AKS_AUTOMATIC=true
            shift
            ;;
        --help|-h)
            usage
            exit 0
            ;;
        *)
            print_error "Unknown option: $1"
            usage
            exit 1
            ;;
    esac
done

# Validate required parameters
if [[ -z "$SUBSCRIPTION" || -z "$LOCATION" || -z "$RESOURCE_GROUP" || -z "$CLUSTER_NAME" ]]; then
    print_error "Missing required parameters"
    usage
    exit 1
fi

# ACR resource ID is only required when not doing cleanup
if [[ "$CLEANUP" == false && -z "$ACR_RESOURCE_ID" ]]; then
    print_error "ACR resource ID is required when not using --cleanup"
    usage
    exit 1
fi

# Validate AKS Automatic options
if [[ "$AKS_AUTOMATIC" == true ]]; then
    if [[ -n "$KUBERNETES_VERSION" ]]; then
        print_error "Kubernetes version cannot be specified with AKS Automatic"
        exit 1
    fi
    if [[ "$NODE_SIZE" != "Standard_DS2_v2" ]]; then
        print_error "Node size cannot be customized with AKS Automatic"
        exit 1
    fi
fi

# Check if Azure CLI is installed
if ! command -v az &> /dev/null; then
    print_error "Azure CLI is not installed. Please install it first."
    print_error "Visit: https://docs.microsoft.com/en-us/cli/azure/install-azure-cli"
    exit 1
fi

# Handle cleanup option
if [[ "$CLEANUP" == true ]]; then
    print_status "Starting cleanup process..."
    print_status "WARNING: This will delete the entire resource group '$RESOURCE_GROUP' and ALL resources within it!"
    
    # Delete resource group
    print_status "Deleting resource group: $RESOURCE_GROUP"
    az group delete --name "$RESOURCE_GROUP" --subscription "$SUBSCRIPTION"
    
    exit 0
fi

# Check if kubectl is installed
if ! command -v kubectl &> /dev/null; then
    print_error "kubectl is not installed. Please install it first."
    print_error "Visit: https://kubernetes.io/docs/tasks/tools/install-kubectl/"
    exit 1
fi

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    print_error "Docker is not installed. Please install it first."
    print_error "Visit: https://docs.docker.com/get-docker/"
    exit 1
fi

# Check if kustomize is available (kubectl has built-in kustomize support)
if ! kubectl kustomize --help &> /dev/null; then
    print_error "kubectl kustomize is not available. Please update kubectl to a newer version."
    exit 1
fi

print_status "Starting cluster-health-monitor build and AKS deployment process"
print_status "Subscription: $SUBSCRIPTION"
print_status "Location: $LOCATION"
print_status "Resource Group: $RESOURCE_GROUP"
print_status "Cluster Name: $CLUSTER_NAME"
print_status "ACR Resource ID: $ACR_RESOURCE_ID"

# Step 1: Validate ACR exists and get access
# Extract ACR name from resource ID for validation and Docker operations
ACR_NAME=${ACR_RESOURCE_ID##*/}
if [[ -z "$ACR_NAME" ]]; then
    print_error "Invalid ACR resource ID format"
    exit 1
fi
print_status "ACR Name: $ACR_NAME"

# Extract ACR resource group from resource ID
ACR_RESOURCE_GROUP=$(echo "$ACR_RESOURCE_ID" | cut -d'/' -f5)
if [[ -z "$ACR_RESOURCE_GROUP" ]]; then
    print_error "Invalid ACR resource ID format - cannot extract resource group"
    exit 1
fi
print_status "ACR Resource Group: $ACR_RESOURCE_GROUP"

# Validate ACR exists and get login server
print_status "Step 1: Validating ACR access..."
ACR_LOGIN_SERVER=$(az acr show --name "$ACR_NAME" --resource-group "$ACR_RESOURCE_GROUP" --query loginServer -o tsv --subscription "$SUBSCRIPTION" 2>/dev/null)
if [[ -z "$ACR_LOGIN_SERVER" ]]; then
    print_error "Cannot access ACR with resource ID: $ACR_RESOURCE_ID"
    print_error "Please check the resource ID and ensure you have permissions"
    exit 1
fi
print_success "ACR validated: $ACR_LOGIN_SERVER"

# Step 2: Build and push container image to ACR
print_status "Step 2: Building and pushing cluster-health-monitor image to ACR..."

# Login to ACR
print_status "Logging into ACR: $ACR_LOGIN_SERVER"
az acr login --name "$ACR_NAME" --subscription "$SUBSCRIPTION"

# Define image name and tag
IMAGE_NAME="cluster-health-monitor"
IMAGE_TAG="latest"
FULL_IMAGE_NAME="$ACR_LOGIN_SERVER/$IMAGE_NAME:$IMAGE_TAG"

print_status "Building Docker image: $FULL_IMAGE_NAME"

# Check if Dockerfile exists
DOCKERFILE_PATH="docker/cluster-health-monitor.Dockerfile"
if [[ ! -f "$DOCKERFILE_PATH" ]]; then
    print_error "Dockerfile not found at: $DOCKERFILE_PATH"
    print_error "Please run this script from the project root directory"
    exit 1
fi

# Build the Docker image
docker build -f "$DOCKERFILE_PATH" -t "$FULL_IMAGE_NAME" .

print_status "Pushing image to ACR: $FULL_IMAGE_NAME"
docker push "$FULL_IMAGE_NAME"

print_success "Container image pushed successfully to ACR"

# Step 3: Create resource group and AKS cluster
# Check if resource group already exists
print_status "Step 3: Creating/validating resource group and AKS cluster..."
print_status "Checking if resource group '$RESOURCE_GROUP' already exists..."
if az group show --name "$RESOURCE_GROUP" --subscription "$SUBSCRIPTION" &> /dev/null; then
    print_success "Resource group '$RESOURCE_GROUP' already exists, skipping creation"
else
    print_status "Creating resource group: $RESOURCE_GROUP"
    az group create --name "$RESOURCE_GROUP" --location "$LOCATION" --subscription "$SUBSCRIPTION"
    print_success "Resource group created successfully"
fi

# Check if AKS cluster already exists
print_status "Checking if AKS cluster '$CLUSTER_NAME' already exists..."
if az aks show --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --subscription "$SUBSCRIPTION" &> /dev/null; then
    print_success "AKS cluster '$CLUSTER_NAME' already exists, skipping creation"
else
    print_status "Creating AKS cluster: $CLUSTER_NAME"
    
    # Build the base AKS create command with common parameters
    CREATE_CMD="az aks create \
        --resource-group $RESOURCE_GROUP \
        --name $CLUSTER_NAME \
        --generate-ssh-keys \
        --location $LOCATION \
        --subscription $SUBSCRIPTION \
        --attach-acr $ACR_RESOURCE_ID"
    
    # Add specific parameters based on cluster type
    if [[ "$AKS_AUTOMATIC" == true ]]; then
        print_status "Using AKS Automatic mode"
        CREATE_CMD="$CREATE_CMD --sku Automatic"
    else
        CREATE_CMD="$CREATE_CMD --node-vm-size $NODE_SIZE --enable-managed-identity"
        
        # Add Kubernetes version if specified (only for regular AKS)
        if [[ -n "$KUBERNETES_VERSION" ]]; then
            CREATE_CMD="$CREATE_CMD --kubernetes-version $KUBERNETES_VERSION"
        fi
    fi
    
    print_status "Running: $CREATE_CMD"
    eval "$CREATE_CMD"
    
    print_success "AKS cluster created successfully"
fi

# Get AKS credentials
print_status "Getting AKS cluster credentials..."
az aks get-credentials --resource-group "$RESOURCE_GROUP" --name "$CLUSTER_NAME" --overwrite-existing --subscription "$SUBSCRIPTION"

# Verify cluster connectivity
print_status "Verifying cluster connectivity..."
if ! kubectl get nodes &> /dev/null; then
    print_error "Failed to connect to AKS cluster"
    exit 1
fi

print_success "Successfully connected to AKS cluster"
kubectl get nodes

# Step 4: Deploy cluster-health-monitor using the already-built image
print_status "Step 4: Deploying cluster-health-monitor..."

# Use manifests directory assuming we're running from project root
MANIFESTS_DIR="manifests/base"

if [[ ! -d "$MANIFESTS_DIR" ]]; then
    print_error "Manifests directory not found at: $MANIFESTS_DIR"
    print_error "Please run this script from the project root directory"
    exit 1
fi

print_status "Using manifests from: $MANIFESTS_DIR"

# Apply the manifests using kustomize
print_status "Applying cluster-health-monitor manifests..."
kubectl apply -k "$MANIFESTS_DIR"

# Wait for deployment to be ready
print_status "Waiting for cluster-health-monitor deployment to be ready..."
kubectl wait --for=condition=available --timeout=300s deployment/cluster-health-monitor -n kube-system

# Show configuration
print_status "Current configuration:"
kubectl get configmap cluster-health-monitor-config -n kube-system -o yaml

print_success "Deployment completed successfully!"

# Summary
echo ""
echo "==============================================="
echo "           DEPLOYMENT SUMMARY"
echo "==============================================="
echo "Subscription:     $SUBSCRIPTION"
echo "Resource Group:   $RESOURCE_GROUP"
echo "AKS Cluster:      $CLUSTER_NAME"
echo "Location:         $LOCATION"
if [[ "$AKS_AUTOMATIC" == true ]]; then
    echo "Cluster Type:     AKS Automatic"
else
    echo "Node Size:        $NODE_SIZE"
    echo "Cluster Type:     Standard AKS"
fi
echo "ACR Resource ID:  $ACR_RESOURCE_ID"
echo "Container Image:  $FULL_IMAGE_NAME"
echo "Namespace:        kube-system"
echo "Deployment:       cluster-health-monitor"
echo "==============================================="
echo ""

# Display image update instructions
print_status "To update the deployment to use your custom image, run:"
print_status "kubectl set image deployment/cluster-health-monitor cluster-health-monitor=$FULL_IMAGE_NAME -n kube-system"
print_status ""

# Display metrics endpoint information
print_status "Cluster Health Monitor metrics are available on port 9800"
print_status "To access metrics, you can port-forward:"
print_status "kubectl port-forward -n kube-system deployment/cluster-health-monitor 9800:9800"
print_status "Then visit: http://localhost:9800/metrics"

print_status "To view logs: kubectl logs -n kube-system deployment/cluster-health-monitor -f"
