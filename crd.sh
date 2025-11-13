#!/bin/bash

controller-gen crd:crdVersions=v1 paths=./apis/... output:crd:stdout > ./manifests/base/crd.yaml
kubectl delete cnh test
kubectl delete crd checknodehealths.chm.azure.com
kubectl apply -f ./manifests/base/crd.yaml
kubectl apply -f ./manifests/examples/checknodehealth-example.yaml
kubectl patch checknodehealth test --subresource=status --type=merge -p='
{
  "status": {
    "startedAt": "2025-10-18T00:12:34Z",
    "finishedAt": "2025-10-18T00:12:57Z",
    "conditions": [
      {
        "type": "Healthy",
        "status": "True", 
        "reason": "ChecksPassed",
        "message": "success",
        "lastTransitionTime": "2025-10-18T00:12:57Z"
      }
    ],
    "results": [
      {
        "name": "PodStartup",
        "status": "Healthy",
        "errorCode": "ServiceUnreachable",
        "message": "Pod started successfully on the node"
      }
    ]
  }
}
'