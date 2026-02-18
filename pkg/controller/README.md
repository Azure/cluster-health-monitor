# Generate Controller's Role YAML

```
# 1. Install controller-gen
go install sigs.k8s.io/controller-tools/cmd/controller-gen@latest

# 2. Generate the role for controller
cd cluster-health-monitor
controller-gen rbac:roleName=checknodehealth-controller paths="./pkg/controller/..." output:rbac:stdout > manifests/base/checknodehealth-controller/role.yaml
```
