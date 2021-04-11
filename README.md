# Micro Application Controller

## About

A controller which validates creates resources from Git based on the creator's permissions on the cluster.

```
apiVersion: argoproj.io/v1alpha1
kind: MicroApplication
metadata:
  name: microapplication-sample
  namespace: sbose
  annotations:
    "generated-creator": "consoledeveloper" 
spec:
  repoURL: https://github.com/sbose78/gitops-samples
  path: developer/new-app
```


